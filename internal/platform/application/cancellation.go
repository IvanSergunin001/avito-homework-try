// Отмена заказа: покупателем и через саппорта.
//
// Сценарий отдельный от оформления, хотя заказ тот же самый: оформление разговаривает с
// заведением, чтобы узнать его решение, а отмена — чтобы сообщить своё. Решение об
// отмене принимает площадка по состоянию заказа, а не заведение по состоянию своей кухни:
// покупатель отменяет заказ, пока продукты не потрачены, и право на это не может зависеть
// от того, отвечает ли сейчас коробка заведения.
//
// Отсюда и порядок: отмена сначала записывается, а потом доезжает до заведения. Обратный
// порядок означал бы, что молчащее заведение не даёт покупателю передумать.

package application

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// cancelledByCustomerReason — чем площадка объясняет заведению отмену покупателем.
//
// У самой отмены причины нет: покупатель передумал и объяснять этого не обязан. Но
// оператор коробки видит в списке заказ, который у него забрали, и «отменён» без единого
// слова он читает как сбой интеграции.
const cancelledByCustomerReason = "Заказ отменён покупателем"

// CancelledOrders — заказы площадки глазами отмены.
//
// Порт свой, а не общий с оформлением и приёмом уведомлений, хотя за всеми тремя одна
// таблица: у отмены два входа с разным правом видеть заказ. Покупатель отменяет только
// свой — и заказ ищется вместе с ним; саппорт разбирает обращение о любом заказе — и
// читает его по одному идентификатору. Один порт на всех означал бы, что чтение «любого
// заказа» доступно и там, где заказ полагается спрашивать вместе с его покупателем.
type CancelledOrders interface {
	// FindForCustomer отдаёт заказ, принадлежащий этому покупателю. Если заказа нет либо
	// он чужой — domain.ErrOrderNotFound.
	FindForCustomer(ctx context.Context, id, customer uuid.UUID) (domain.Order, error)
	// Find отдаёт заказ по одному лишь идентификатору — так его читает саппорт, к
	// которому обращение приходит от заведения, а не от покупателя. Если заказа нет —
	// domain.ErrOrderNotFound.
	Find(ctx context.Context, id uuid.UUID) (domain.Order, error)
	// SaveStatus записывает новое состояние заказа вместе с журналом его переходов.
	SaveStatus(ctx context.Context, order domain.Order) error
}

// OrderCancellations — отмена принятого заказа по ту сторону партнёрского контракта.
//
// Порт отдельный от OrderIntake: приём заказа — это вопрос, ответ на который решает
// судьбу заказа, а отмена — извещение о уже принятом решении, и путать их значило бы
// однажды поставить ответ покупателю в зависимость от того, что ответила коробка.
type OrderCancellations interface {
	// Cancel сообщает заведению, живущему по адресу baseURL, что заказ отменён, и
	// называет причину для оператора.
	//
	// Реализация обязана ограничивать себя сама: без таймаута заведение, принявшее
	// соединение и замолчавшее, задержало бы ответ покупателю, которому уже нечего ждать.
	Cancel(ctx context.Context, baseURL string, order domain.Order, reason string) error
}

// Cancellation — отмена заказа покупателем и саппортом.
//
// Сценарий один на обоих инициаторов: отменяется один и тот же заказ по одной и той же
// матрице переходов, и разница между ними — только в том, кто вправе отменить заказ в
// каком состоянии и обязан ли он объяснить, почему. Оба ответа даёт агрегат.
type Cancellation struct {
	partners Partners
	orders   CancelledOrders
	boxes    OrderCancellations
	log      *slog.Logger
}

// NewCancellation собирает отмену над заведениями, хранилищем заказов и отменой на
// стороне заведения.
func NewCancellation(
	partners Partners, orders CancelledOrders, boxes OrderCancellations, log *slog.Logger,
) *Cancellation {
	return &Cancellation{partners: partners, orders: orders, boxes: boxes, log: log}
}

// ByCustomer отменяет заказ по требованию покупателя.
//
// Причины у такой отмены нет: покупатель передумал, и объяснять это он никому не обязан.
// Из `cooking` и дальше отмена не проходит — продукты потрачены, — и покупатель получает
// отказ матрицы, который поверхность переводит в понятный ему код.
//
// Повторное нажатие «Отменить» отдаёт тот же отменённый заказ и ничего не трогает:
// ретрай кнопки не должен ломаться, а заведение не должно узнать об одной отмене дважды.
func (c *Cancellation) ByCustomer(ctx context.Context, id, customer uuid.UUID) (domain.Order, error) {
	order, err := c.orders.FindForCustomer(ctx, id, customer)
	if err != nil {
		return domain.Order{}, err
	}

	cancelled, err := order.CancelByCustomer(now())
	if err != nil {
		return domain.Order{}, err
	}
	if !cancelled {
		return order, nil
	}

	return c.apply(ctx, order, cancelledByCustomerReason)
}

// BySupport отменяет заказ по обращению заведения, разобранному сотрудником площадки.
//
// Так и только так отменяется заказ, который уже готовят: партнёрский контракт этого не
// позволяет, потому что продукты списаны, покупателю нужна компенсация, а отмена без
// разбора — повод для злоупотреблений. Причина обязательна: её увидит покупатель, и
// «заказ отменён» без объяснения — это обращение в поддержку, а не ответ.
//
// Заказ, который прожил свою жизнь, не отменяется и здесь: доставленный заказ отменять
// нечего, а отменённый уже отменён — и переписывать чужую причину поверх записанной
// саппорту незачем.
func (c *Cancellation) BySupport(ctx context.Context, id uuid.UUID, reason string) (domain.Order, error) {
	order, err := c.orders.Find(ctx, id)
	if err != nil {
		return domain.Order{}, err
	}

	if err := order.Cancel(domain.BySupport, reason, now()); err != nil {
		return domain.Order{}, err
	}

	return c.apply(ctx, order, order.CancellationReason)
}

// apply записывает отмену и доносит её до заведения.
//
// Записывается она первой: заказ, отменённый в базе, но не доехавший до коробки, разберёт
// человек по логу и по журналу состояний, а заказ, отменённый у заведения, но не у
// площадки, покупатель увидит живым — и будет ждать еду, которую никто не готовит.
func (c *Cancellation) apply(
	ctx context.Context, order domain.Order, reason string,
) (domain.Order, error) {
	if err := c.orders.SaveStatus(ctx, order); err != nil {
		return domain.Order{}, err
	}

	c.tellTheRestaurant(ctx, order, reason)

	return order, nil
}

// tellTheRestaurant доносит отмену до заведения.
//
// Неудача остаётся в логе и до покупателя не доезжает: отменить заказ он вправе
// независимо от того, отвечает ли сейчас коробка, и отказа на этот случай клиентская
// спецификация не предусматривает вовсе. Отказать может и само заведение — оно успело
// начать готовку, о которой площадка ещё не знает; это тот самый случай, ради которого
// существует саппорт, и разбирает его человек, а не повтор вызова.
func (c *Cancellation) tellTheRestaurant(ctx context.Context, order domain.Order, reason string) {
	if err := c.deliver(ctx, order, reason); err != nil {
		c.log.WarnContext(ctx, "cancellation was not delivered to the restaurant",
			slog.String("order_id", order.ID.String()),
			slog.String("restaurant_id", order.RestaurantID.String()),
			slog.Any("error", err))

		return
	}

	c.log.InfoContext(ctx, "order cancelled and the restaurant is told",
		slog.String("order_id", order.ID.String()),
		slog.String("cancelled_by", string(order.CancelledBy)))
}

// deliver доносит отмену до коробки заведения, которое выполняет заказ.
//
// Заведение, которое площадка успела отключить, здесь и не находится: адреса коробки у
// неё больше нет, и звонить некуда — это такая же недоставленная отмена, как молчащая
// коробка, и обходится с ней вызывающий одинаково.
func (c *Cancellation) deliver(ctx context.Context, order domain.Order, reason string) error {
	partner, err := c.partners.Find(ctx, order.RestaurantID)
	if err != nil {
		return err
	}

	return c.boxes.Cancel(ctx, partner.BaseURL, order, reason)
}
