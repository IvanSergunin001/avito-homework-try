package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// OrderPage — окно списка заказов вместе с общим их числом: без него оператор не знает,
// есть ли что листать дальше.
type OrderPage struct {
	// Items — заказы запрошенного окна, свежие первыми.
	Items []domain.Order
	// Total — сколько заказов у коробки всего, а не сколько попало в окно.
	Total int64
}

// IncomingOrders — хранилище входящих заказов коробки.
//
// Приём и решение оператора выражены операциями с обратным вызовом, а не парой
// «прочитать — записать»: правило принимает предметная область, но применить его нужно
// внутри одной транзакции. Между чтением меню и списанием остатка не должно быть окна, в
// котором позиция уходит в стоп-лист, а между чтением заказа и его подтверждением —
// окна, в котором тот же заказ подтверждает оператор с соседнего рабочего места.
type IncomingOrders interface {
	// Accept принимает заказ: читает нужные позиции меню, отдаёт их решению decide и,
	// если заказ принят, списывает остатки и записывает заказ — всё одной транзакцией.
	//
	// Второй результат отвечает, завели ли заказ сейчас: ложь означает, что заказ с этим
	// ключом идемпотентности коробка уже принимала, и отдан результат первой попытки.
	Accept(
		ctx context.Context,
		request domain.OrderRequest,
		decide func(menu map[string]domain.MenuItem) (domain.Order, error),
	) (domain.Order, bool, error)

	// Page отдаёт окно списка заказов и общее их число.
	Page(ctx context.Context, window Window) (OrderPage, error)

	// Confirm применяет к заказу решение оператора взяться за него. Если заказа нет —
	// domain.ErrOrderNotFound.
	Confirm(ctx context.Context, id uuid.UUID, decide func(order *domain.Order) error) (domain.Order, error)

	// Reject применяет к заказу отказ оператора и возвращает в меню списанные под заказ
	// остатки. Если заказа нет — domain.ErrOrderNotFound.
	Reject(ctx context.Context, id uuid.UUID, decide func(order *domain.Order) error) (domain.Order, error)
}

// Orders — входящие заказы коробки: приём по партнёрскому контракту и рассмотрение
// оператором.
//
// Сценарий один на обе поверхности намеренно: заказ, который приняла коробка, и заказ,
// который видит оператор, — один и тот же заказ, и расхождение между ними было бы
// дефектом, а не свойством.
type Orders struct {
	orders IncomingOrders
	hours  domain.WorkingHours
}

// NewOrders собирает сценарий поверх хранилища заказов. hours — часы работы заведения:
// вне их коробка заказы не принимает.
func NewOrders(orders IncomingOrders, hours domain.WorkingHours) *Orders {
	return &Orders{orders: orders, hours: hours}
}

// Accept принимает заказ, переданный площадкой.
//
// Второй результат отвечает, завели ли заказ сейчас: повторная отправка того же заказа
// не готовится дважды — коробка отдаёт результат первой попытки и остаток второй раз не
// списывает.
func (o *Orders) Accept(ctx context.Context, request domain.OrderRequest) (domain.Order, bool, error) {
	return o.orders.Accept(ctx, request, func(menu map[string]domain.MenuItem) (domain.Order, error) {
		// Идентификатор выдаёт коробка, а не площадка: заказ площадки и заказ коробки —
		// это два заказа в двух системах, и общий на двоих идентификатор означал бы, что
		// коробка живёт по чужой нумерации. uuid v7 монотонный — клиентский первичный
		// ключ не разносит вставки по индексу.
		id, err := uuid.NewV7()
		if err != nil {
			return domain.Order{}, fmt.Errorf("выдать идентификатор заказа: %w", err)
		}

		// Время приёма округляется до микросекунды: столько хранит база, и повтор
		// доставки, которому отвечают уже прочитанным заказом, иначе получил бы время,
		// отличающееся от отданного первой попытке, — площадка увидела бы два разных
		// ответа на один и тот же заказ.
		return domain.AcceptOrder(id, request, menu, o.hours, time.Now().Truncate(time.Microsecond))
	})
}

// Page отдаёт страницу входящих заказов: состав, адрес и состояние каждого.
func (o *Orders) Page(ctx context.Context, window Window) (OrderPage, error) {
	return o.orders.Page(ctx, window)
}

// Confirm — оператор берётся за заказ.
func (o *Orders) Confirm(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return o.orders.Confirm(ctx, id, func(order *domain.Order) error {
		return order.Confirm()
	})
}

// Reject — заведение не берётся за заказ и объясняет причину.
func (o *Orders) Reject(ctx context.Context, id uuid.UUID, reason string) (domain.Order, error) {
	return o.orders.Reject(ctx, id, func(order *domain.Order) error {
		return order.Reject(reason)
	})
}
