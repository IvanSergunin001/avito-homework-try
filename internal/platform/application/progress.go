package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// StatusNotice — уведомление заведения о том, что заказ продвинулся.
type StatusNotice struct {
	// Status — состояние, о котором сообщает заведение.
	Status domain.OrderStatus
	// Reason — почему заведение отменило заказ; у остальных уведомлений пуста.
	Reason string
	// OccurredAt — когда состояние сменилось у заведения, по его часам.
	OccurredAt time.Time
}

// NotifiedOrders — заказы площадки глазами приёма уведомлений.
//
// Порт свой, а не общий с оформлением, хотя за обоими одна таблица: оформлению нужен
// заказ вместе с его покупателем, приёму уведомлений — вместе с заведением, которое его
// выполняет. Один порт на оба сценария означал бы, что приём уведомлений умеет читать
// заказы по покупателю, — то есть заведение могло бы дотянуться до заказа, который ему
// не передавали.
type NotifiedOrders interface {
	// FindForRestaurant отдаёт заказ, который выполняет это заведение. Если заказа нет
	// либо он не этого заведения — domain.ErrOrderNotFound.
	FindForRestaurant(ctx context.Context, id, restaurant uuid.UUID) (domain.Order, error)
	// SaveStatus записывает новое состояние заказа вместе с журналом его переходов.
	SaveStatus(ctx context.Context, order domain.Order) error
}

// Progress — продвижение заказа уведомлениями заведения.
//
// Обратной стороны — опроса статусов площадкой — нет и не появится: заведение сообщает о
// заказе само, а опрос стучался бы во все подключённые заведения постоянно и всё равно
// узнавал бы новости с задержкой в интервал опроса.
type Progress struct {
	orders NotifiedOrders
	log    *slog.Logger
}

// NewProgress собирает приём уведомлений над хранилищем заказов.
func NewProgress(orders NotifiedOrders, log *slog.Logger) *Progress {
	return &Progress{orders: orders, log: log}
}

// Notify применяет уведомление заведения и отдаёт заказ в том состоянии, в котором он
// после этого остался.
//
// Заведение приходит опознанным по своему партнёрскому ключу, а не названным в запросе:
// заказ ищется среди заказов этого заведения, поэтому уведомить о чужом заказе нельзя
// даже зная его идентификатор — такого заказа для звонящего просто нет.
//
// Состояние может и не измениться: повтор, опоздавшее уведомление и уведомление о заказе,
// который уже прожил свою жизнь, принимаются и ничего не меняют. Это не отказ, и заведению
// отвечают тем состоянием, которое у заказа действительно есть, — по нему видно, что его
// уведомление опоздало, и повторять его незачем.
func (p *Progress) Notify(
	ctx context.Context, partner domain.Partner, id uuid.UUID, notice StatusNotice,
) (domain.Order, error) {
	order, err := p.orders.FindForRestaurant(ctx, id, partner.ID)
	if err != nil {
		return domain.Order{}, err
	}

	// Время перехода — часы площадки, а не присланное заведением `occurred_at`: часы двух
	// организаций не сверены, и заказ, у которого «изменён» раньше, чем «оформлен», —
	// это не история, а расхождение часов. Само `occurred_at` уходит в лог: по нему
	// человек, разбирающий жалобу «статус приехал через полчаса», видит, где уведомление
	// пролежало — в очереди заведения или у площадки.
	applied, err := order.ApplyNotification(notice.Status, notice.Reason, now())
	if err != nil {
		return domain.Order{}, err
	}
	if !applied {
		p.log.InfoContext(ctx, "status notification changed nothing",
			slog.String("order_id", order.ID.String()),
			slog.String("notified_status", string(notice.Status)),
			slog.String("order_status", string(order.Status)),
			slog.Time("occurred_at", notice.OccurredAt))

		return order, nil
	}

	if err := p.orders.SaveStatus(ctx, order); err != nil {
		return domain.Order{}, err
	}

	p.log.InfoContext(ctx, "order status advanced by the restaurant",
		slog.String("order_id", order.ID.String()),
		slog.String("order_status", string(order.Status)),
		slog.Time("occurred_at", notice.OccurredAt))

	return order, nil
}
