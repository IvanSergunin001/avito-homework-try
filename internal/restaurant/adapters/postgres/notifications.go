package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// Notifications — очередь уведомлений площадке в схеме `restaurant`.
//
// Строки в очередь кладёт не эта таблица-хранилище, а тот, кто меняет статус заказа: смена
// статуса и уведомление о ней записываются одной транзакцией (см. Orders). Здесь остаётся
// вторая половина работы — выдать доставщику то, чему пора ехать, и записать, чем
// закончилась попытка.
type Notifications struct {
	db *sql.DB
}

// NewNotifications собирает очередь уведомлений поверх подключения к базе коробки.
func NewNotifications(db *sql.DB) *Notifications {
	return &Notifications{db: db}
}

// Due отдаёт уведомления, которым к моменту at пора ехать: не доставленные, по которым
// не кончились попытки и чья пауза уже прошла.
//
// Строки не запираются `FOR UPDATE`: блокировку пришлось бы держать всё время обращения к
// площадке, то есть занять подключение к базе на время сетевого обмена с чужой системой.
// Цена отказа от блокировки — второй экземпляр коробки, который увёз бы то же уведомление
// второй раз, а это ровно тот случай, ради которого приём уведомлений на площадке
// идемпотентен: повторное уведомление о том же статусе принимается и ничего не меняет.
func (n *Notifications) Due(
	ctx context.Context, at time.Time, limit int,
) ([]domain.PendingNotification, error) {
	rows, err := n.db.QueryContext(ctx, `
		SELECT notification.order_id, ordered.platform_order_id, notification.status,
		       notification.reason, notification.occurred_at, notification.attempts
		FROM restaurant.status_notifications AS notification
		JOIN restaurant.orders AS ordered ON ordered.id = notification.order_id
		WHERE notification.delivered_at IS NULL
		  AND notification.failed_at IS NULL
		  AND notification.next_attempt_at <= $1
		ORDER BY notification.next_attempt_at, notification.order_id
		LIMIT $2`, at, limit)
	if err != nil {
		return nil, fmt.Errorf("прочитать очередь уведомлений: %w", err)
	}
	defer func() { _ = rows.Close() }()

	pending := make([]domain.PendingNotification, 0, limit)
	for rows.Next() {
		var (
			notification domain.PendingNotification
			status       string
		)

		err := rows.Scan(&notification.OrderID, &notification.PlatformOrderID, &status,
			&notification.Reason, &notification.OccurredAt, &notification.Attempts)
		if err != nil {
			return nil, fmt.Errorf("прочитать уведомление: %w", err)
		}

		notification.Status = domain.OrderStatus(status)
		pending = append(pending, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перебрать очередь уведомлений: %w", err)
	}

	return pending, nil
}

// Delivered отмечает уведомление доставленным: площадка узнала о новом статусе, и везти
// его больше некуда.
//
// Строка не удаляется: по ней видно, что и когда доехало, — а это первое, что спрашивают,
// разбирая жалобу «у покупателя старый статус». Очередь, которая стирает свою историю,
// отвечает на этот вопрос молчанием.
func (n *Notifications) Delivered(
	ctx context.Context, notification domain.PendingNotification, at time.Time,
) error {
	return n.settle(ctx, notification, `
		UPDATE restaurant.status_notifications
		SET attempts = attempts + 1, delivered_at = $3, last_error = ''
		WHERE order_id = $1 AND status = $2`, at)
}

// Postponed записывает неудачную попытку и назначает следующую на retryAt.
func (n *Notifications) Postponed(
	ctx context.Context, notification domain.PendingNotification, retryAt time.Time, reason string,
) error {
	return n.settle(ctx, notification, `
		UPDATE restaurant.status_notifications
		SET attempts = attempts + 1, next_attempt_at = $3, last_error = $4
		WHERE order_id = $1 AND status = $2`, retryAt, reason)
}

// Exhausted записывает последнюю неудачную попытку: повторять больше нечем.
//
// Уведомление остаётся в очереди недоставленным, с отметкой времени и с ответом площадки:
// исчерпание попыток — это не «доставлено» и не «забыто», а повод разобраться руками, и
// найти такие строки должно быть можно одним запросом.
func (n *Notifications) Exhausted(
	ctx context.Context, notification domain.PendingNotification, at time.Time, reason string,
) error {
	return n.settle(ctx, notification, `
		UPDATE restaurant.status_notifications
		SET attempts = attempts + 1, failed_at = $3, last_error = $4
		WHERE order_id = $1 AND status = $2`, at, reason)
}

// settle применяет к строке очереди итог попытки доставки.
//
// Уведомление опознаётся парой «заказ + статус» — тем же ключом, которым оно попало в
// очередь: собственного идентификатора у него нет, потому что в каждое состояние заказ
// приходит ровно один раз.
func (n *Notifications) settle(
	ctx context.Context, notification domain.PendingNotification, query string, args ...any,
) error {
	arguments := append([]any{notification.OrderID, string(notification.Status)}, args...)

	if _, err := n.db.ExecContext(ctx, query, arguments...); err != nil {
		return fmt.Errorf("записать итог доставки уведомления о заказе %s: %w", notification.OrderID, err)
	}

	return nil
}
