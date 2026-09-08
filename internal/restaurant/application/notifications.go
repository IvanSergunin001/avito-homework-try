package application

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// deliveryBatchSize — сколько уведомлений коробка увозит за один проход.
//
// Предел нужен не ради базы, а ради площадки: коробка, простоявшая час без сети, иначе
// вывалила бы на неё всю накопившуюся очередь одним залпом — ровно в тот момент, когда
// площадка только поднялась. Остаток уедет следующим проходом, до которого секунды.
const deliveryBatchSize = 100

// NotificationQueue — очередь уведомлений в базе коробки.
//
// Порт описан итогами попытки, а не парой «прочитать — записать»: что делать с
// недоставленным уведомлением, решает правило повторов, и хранилищу остаётся записать
// решение — доехало, отложено до такого-то времени, попытки кончились.
type NotificationQueue interface {
	// Due отдаёт уведомления, которым к моменту at пора ехать, не больше limit за раз.
	Due(ctx context.Context, at time.Time, limit int) ([]domain.PendingNotification, error)
	// Delivered отмечает, что площадка приняла уведомление.
	Delivered(ctx context.Context, notification domain.PendingNotification, at time.Time) error
	// Postponed записывает неудачную попытку и назначает следующую на retryAt.
	Postponed(ctx context.Context, notification domain.PendingNotification, retryAt time.Time, reason string) error
	// Exhausted записывает последнюю неудачную попытку: повторять больше нечем.
	Exhausted(ctx context.Context, notification domain.PendingNotification, at time.Time, reason string) error
}

// PlatformNotifier — площадка по ту сторону партнёрского контракта.
//
// Порт — про один вызов, а не про очередь: повторы, паузы и счёт попыток живут здесь, в
// сценарии, и не должны зависеть от того, вспомнит ли о них тот, кто пишет клиента.
type PlatformNotifier interface {
	// Notify доставляет площадке одно уведомление. Ошибка означает, что площадка его не
	// приняла, — по любой причине: сеть, отказ, ответ не по контракту.
	Notify(ctx context.Context, notification domain.PendingNotification) error
}

// Notifications — доставка площадке уведомлений о смене статуса заказа.
//
// Уведомления кладёт в очередь тот, кто меняет статус, — той же транзакцией, что и сам
// статус. Здесь только доставка, и это разделение и есть вся надёжность: оператор не ждёт
// площадку, нажимая кнопку, а площадка узнаёт о заказе даже тогда, когда в момент нажатия
// она была недоступна.
type Notifications struct {
	queue    NotificationQueue
	platform PlatformNotifier
	retry    domain.RetryPolicy
	log      *slog.Logger
}

// NewNotifications собирает доставку над очередью коробки и площадкой.
func NewNotifications(
	queue NotificationQueue, platform PlatformNotifier, retry domain.RetryPolicy, log *slog.Logger,
) *Notifications {
	return &Notifications{queue: queue, platform: platform, retry: retry, log: log}
}

// Deliver везёт площадке все уведомления, которым к моменту at пора ехать.
//
// Момент приходит параметром, а не берётся из часов: по нему решается, прошла ли пауза
// перед повтором, — и это то самое правило, которое иначе проверялось бы ожиданием в
// тесте. Служба передаёт сюда своё «сейчас», тест — момент, в который повтор должен
// поехать.
//
// Неудача одного уведомления не прекращает проход: уведомления независимы, и заказ,
// который площадка отказалась принять, не повод задержать остальные. Наружу поднимается
// только неудача самой очереди — с базой коробки разговаривать больше некому.
func (n *Notifications) Deliver(ctx context.Context, at time.Time) error {
	due, err := n.queue.Due(ctx, at, deliveryBatchSize)
	if err != nil {
		return fmt.Errorf("выбрать уведомления к доставке: %w", err)
	}

	for _, notification := range due {
		if err := n.platform.Notify(ctx, notification); err != nil {
			if err := n.postpone(ctx, notification, at, err); err != nil {
				return err
			}

			continue
		}

		if err := n.queue.Delivered(ctx, notification, at); err != nil {
			return err
		}

		n.log.InfoContext(ctx, "order status notification delivered",
			slog.String("order_id", notification.OrderID.String()),
			slog.String("platform_order_id", notification.PlatformOrderID.String()),
			slog.String("status", string(notification.Status)),
			slog.Int("attempt", notification.Attempts+1))
	}

	return nil
}

// RunPeriodically развозит накопившиеся уведомления каждые every, пока жив ctx.
//
// Это вся фоновая доставка целиком: тонкая обёртка над Deliver. Своего правила у тика нет,
// и проверять его ожиданием срабатывания незачем — тест, который спит в надежде на таймер,
// проверяет планировщик Go, а не поведение коробки.
func (n *Notifications) RunPeriodically(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := n.Deliver(ctx, time.Now()); err != nil {
				n.log.ErrorContext(ctx, "status notification delivery failed", slog.Any("error", err))
			}
		}
	}
}

// postpone решает судьбу уведомления, которое площадка не приняла: повторить позже или
// сдаться.
//
// Исчерпание попыток остаётся видимым дважды — строкой в очереди и записью в логе, — и это
// не дублирование: по логу о нём узнаёт дежурный в тот же час, по данным его находит тот,
// кто через день разбирает жалобу на застрявший статус.
func (n *Notifications) postpone(
	ctx context.Context, notification domain.PendingNotification, at time.Time, failure error,
) error {
	attempts := notification.Attempts + 1

	delay, again := n.retry.NextAttempt(attempts)
	if !again {
		n.log.ErrorContext(ctx, "status notification delivery attempts exhausted",
			slog.String("order_id", notification.OrderID.String()),
			slog.String("platform_order_id", notification.PlatformOrderID.String()),
			slog.String("status", string(notification.Status)),
			slog.Int("attempts", attempts),
			slog.Any("error", failure))

		return n.queue.Exhausted(ctx, notification, at, failure.Error())
	}

	n.log.WarnContext(ctx, "status notification delivery failed, retrying later",
		slog.String("order_id", notification.OrderID.String()),
		slog.String("platform_order_id", notification.PlatformOrderID.String()),
		slog.String("status", string(notification.Status)),
		slog.Int("attempts", attempts),
		slog.Duration("retry_in", delay),
		slog.Any("error", failure))

	return n.queue.Postponed(ctx, notification, at.Add(delay), failure.Error())
}
