package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// errOrderIsDone — заказ, который к моменту решения уже прожил свою жизнь. Не отказ и не
// поломка: между тем, как автопилот перечислил заказы, и тем, как дошёл до этого,
// оператор мог завершить его сам.
var errOrderIsDone = errors.New("заказ уже прошёл свой путь до конца")

// Autopilot — продвижение заказов по таймеру.
//
// Он существует ради демонстрации: пройти путь покупателя целиком, не переключаясь на
// рабочее место оператора и не дёргая его ручки руками. В эксплуатации статусы двигает
// оператор — он единственный, кто знает, что происходит на кухне, — поэтому автопилот по
// умолчанию выключен.
//
// Выключенность — свойство самого автопилота, а не того, кто его запускает: иначе флаг
// пришлось бы проверять в каждом месте запуска, и достаточно забыть об этом в одном,
// чтобы заказы демо-заведения поехали сами у настоящего заведения.
type Autopilot struct {
	orders  IncomingOrders
	enabled bool
	log     *slog.Logger
}

// NewAutopilot собирает автопилот над хранилищем заказов. enabled — флаг конфигурации:
// ложь означает, что заказы стоят, пока их не двинет оператор.
func NewAutopilot(orders IncomingOrders, enabled bool, log *slog.Logger) *Autopilot {
	return &Autopilot{orders: orders, enabled: enabled, log: log}
}

// Advance двигает каждый незавершённый заказ на один шаг пути.
//
// Один шаг за проход, а не весь путь сразу: демонстрация показывает переходы так, как их
// увидел бы покупатель, — «готовится», потом «в доставке», потом «доставлен», — а заказ,
// проскочивший весь путь мгновенно, не показал бы ни одного из них.
//
// Каждый заказ читается заново под блокировкой и двигается своей транзакцией: между
// перечислением и решением помещается оператор, который взял этот заказ сам. Неудача
// одного заказа не прекращает проход — остальные к ней отношения не имеют.
func (a *Autopilot) Advance(ctx context.Context) error {
	if !a.enabled {
		return nil
	}

	ids, err := a.orders.InWork(ctx, domain.StatusesInWork())
	if err != nil {
		return err
	}

	for _, id := range ids {
		order, err := a.orders.Advance(ctx, id, advanceOneStep)
		switch {
		case errors.Is(err, errOrderIsDone), errors.Is(err, domain.ErrOrderNotFound):
			continue
		case err != nil:
			a.log.ErrorContext(ctx, "auto advance failed",
				slog.String("order_id", id.String()), slog.Any("error", err))

			continue
		}

		a.log.InfoContext(ctx, "order advanced by the timer",
			slog.String("order_id", order.ID.String()),
			slog.String("status", string(order.Status)))
	}

	return nil
}

// RunPeriodically двигает заказы каждые every, пока жив ctx. Выключенный автопилот не
// заводит таймера вовсе и говорит об этом в лог: молча ничего не делающая фоновая работа
// неотличима от сломанной.
func (a *Autopilot) RunPeriodically(ctx context.Context, every time.Duration) {
	if !a.enabled {
		a.log.InfoContext(ctx, "auto advance is off, orders wait for the operator")

		return
	}

	a.log.InfoContext(ctx, "auto advance is on, orders move by the timer",
		slog.Duration("interval", every))

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.Advance(ctx); err != nil {
				a.log.ErrorContext(ctx, "scheduled auto advance failed", slog.Any("error", err))
			}
		}
	}
}

// advanceOneStep двигает заказ на следующий шаг его пути — тот, который назвала бы
// предметная область, спроси её оператор.
func advanceOneStep(order *domain.Order) error {
	next, ok := order.Next()
	if !ok {
		return errOrderIsDone
	}

	return order.Advance(next)
}
