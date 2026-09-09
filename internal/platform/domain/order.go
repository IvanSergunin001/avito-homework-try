package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// addressMaxLength, orderItemsMax, itemQuantityMax и reasonMaxLength — пределы,
	// объявленные клиентской спецификацией. Длины считаются в знаках, а не в байтах: адрес
	// пишут кириллицей, и байтовый предел обрезал бы московский адрес вдвое раньше
	// обещанного.
	addressMaxLength = 500
	orderItemsMax    = 100
	itemQuantityMax  = 100
	reasonMaxLength  = 500
)

// OrderStatus — состояние заказа на площадке.
//
// Названия совпадают с теми, которыми заказ живёт в коробке заведения: заведение сообщает
// площадке о своих переходах, и два словаря для одних и тех же состояний означали бы
// перевод на каждой границе — и расхождение при первой же правке одного из них.
type OrderStatus string

const (
	// Pending — заказ записан площадкой, но заведение о нём ещё не знает.
	Pending OrderStatus = "pending"
	// UnderReview — заведение приняло заказ и решает, берётся ли за него.
	UnderReview OrderStatus = "under_review"
	// Confirmed — заведение взялось за заказ.
	Confirmed OrderStatus = "confirmed"
	// Cooking — заказ готовят: с этого момента продукты потрачены.
	Cooking OrderStatus = "cooking"
	// Delivering — заказ везут покупателю.
	Delivering OrderStatus = "delivering"
	// Completed — заказ доставлен.
	Completed OrderStatus = "completed"
	// Cancelled — заказ отменён, кем бы отмена ни была начата.
	Cancelled OrderStatus = "cancelled"
)

// Actor — кто требует перевести заказ в новое состояние.
//
// Инициатор — часть правила, а не пометка в журнале: один и тот же переход одному
// разрешён, другому нет. Отменить готовящийся заказ может саппорт, но не покупатель, и
// матрица без инициатора этой разницы выразить не может.
type Actor string

const (
	// ByCustomer — покупатель, оформивший заказ.
	ByCustomer Actor = "customer"
	// ByRestaurant — заведение: синхронный ответ на переданный заказ и уведомления о том,
	// что с заказом происходит дальше.
	ByRestaurant Actor = "restaurant"
	// BySupport — сотрудник площадки, разбирающий обращение. Заведение после начала
	// готовки отменяет заказ не само, а через него, поэтому это самостоятельный
	// инициатор, а не разновидность заведения.
	BySupport Actor = "support"
)

// transitions — матрица переходов заказа: из какого состояния, в какое и по чьему
// требованию его можно перевести.
//
// Матрица объявлена данными, а не разбросана по условиям в коде, потому что читать её
// приходится целиком: вопрос «кто и когда вправе отменить заказ» задаётся о всей таблице
// сразу, а не о каком-то одном переходе. Перехода «в то же самое состояние» в ней нет ни у
// кого: повторное уведомление ничего не меняет и потому не разрешено.
//
// Отмена расписана по инициаторам, и это и есть правила отмены: покупатель отменяет заказ,
// пока продукты не потрачены (до `cooking`), заведение отказывается от заказа, пока не
// начало готовить, а с началом готовки отмена остаётся только у саппорта — продукты
// списаны, и такой случай разбирает человек.
var transitions = map[OrderStatus]map[OrderStatus][]Actor{
	Pending:     {UnderReview: {ByRestaurant}, Cancelled: {ByCustomer, BySupport}},
	UnderReview: {Confirmed: {ByRestaurant}, Cancelled: {ByCustomer, ByRestaurant, BySupport}},
	Confirmed:   {Cooking: {ByRestaurant}, Cancelled: {ByCustomer, ByRestaurant, BySupport}},
	Cooking:     {Delivering: {ByRestaurant}, Cancelled: {BySupport}},
	Delivering:  {Completed: {ByRestaurant}, Cancelled: {BySupport}},
	// Терминальные состояния: строка пуста, и выйти из них нельзя никому. Пустая строка
	// в матрице стоит явно — по ней видно, что состояние учтено, а не забыто.
	Completed: {},
	Cancelled: {},
}

// IsTerminal — заказ прожил свою жизнь: переходов из этого состояния нет ни у кого.
//
// Терминальность не отдельный список, а свойство матрицы: два списка разошлись бы при
// первой же правке, и заказ оказался бы либо вечным, либо мёртвым раньше времени.
func (s OrderStatus) IsTerminal() bool {
	return len(transitions[s]) == 0
}

// OrderItem — позиция заказа: снимок товара на момент оформления.
//
// Название и цена хранятся копией, а не ссылкой на товар каталога: заказ не должен
// меняться задним числом, когда заведение поднимает цену или убирает позицию из меню, —
// иначе в истории покупателя оказалась бы сумма, которой он не видел, а у исчезнувшей
// позиции пропало бы и название.
type OrderItem struct {
	// ProductID — товар площадки, из которого собрана строка заказа.
	ProductID uuid.UUID
	// ExternalID — чем эта позиция зовётся в системе заведения. Он лежит в снимке, а не
	// берётся из каталога в момент передачи заказа: заказ уезжает заведению тем составом
	// и по тем ценам, которые видел покупатель, а каталог к этому моменту мог измениться.
	ExternalID string
	// Name — название товара на момент оформления заказа.
	Name string
	// Price — цена товара на момент оформления заказа, в копейках.
	Price Money
	// Quantity — сколько единиц заказали.
	Quantity int
}

// Cost — сколько стоит эта строка заказа.
func (i OrderItem) Cost() Money {
	return i.Price * Money(i.Quantity)
}

// Order — заказ покупателя: корень агрегата. Позиции живут внутри него и отдельной жизни
// не имеют, а переходы между состояниями — методы заказа, а не правила обработчиков:
// правило, записанное в обработчике, действует только на том пути, где его вспомнили.
type Order struct {
	// ID — идентификатор заказа, выданный клиентом (uuid v7).
	ID uuid.UUID
	// CustomerID — покупатель, которому принадлежит заказ: по нему заказ и показывается
	// только ему.
	CustomerID uuid.UUID
	// RestaurantID — заведение, которое выполняет заказ.
	RestaurantID uuid.UUID
	// Status — состояние заказа.
	Status OrderStatus
	// Address — адрес доставки в свободной форме.
	Address string
	// Items — состав заказа снимком на момент оформления.
	Items []OrderItem
	// Total — сумма заказа, посчитанная площадкой по своему каталогу.
	Total Money
	// RequestHash — отпечаток запроса, которым заказ оформлен: по нему повтор отличается
	// от переиспользования идентификатора заказа под другой заказ (см. idempotency.go).
	RequestHash string
	// CancelledBy — кто отменил заказ; у неотменённого пуст.
	CancelledBy Actor
	// CancellationReason — почему заказ отменён; у неотменённого пуста.
	CancellationReason string
	// CreatedAt — когда покупатель оформил заказ.
	CreatedAt time.Time
	// UpdatedAt — когда заказ последний раз менял состояние.
	UpdatedAt time.Time
}

// PlaceOrder собирает заказ покупателя и считает его сумму.
//
// Сумму считает площадка, а клиенту она не верит вовсе: присланная клиентом сумма сюда не
// приезжает даже значением, поэтому «не принять» её невозможно забыть. Цены и названия
// приходят из каталога площадки и остаются в заказе снимком.
//
// Заказ рождается в `pending`: он записан, но заведение о нём ещё не знает, и покупателю
// это состояние честно показывается — обещать «принят», не спросив заведение, площадка
// не вправе.
func PlaceOrder(
	id, customerID, restaurantID uuid.UUID, address string, items []OrderItem, at time.Time,
) (Order, error) {
	// Пробелы по краям — след ввода, а не часть адреса: адрес «  » адресом не является.
	address = strings.TrimSpace(address)

	switch {
	case id == uuid.Nil:
		return Order{}, InvalidOrderError{Reason: "order_id: обязательное поле"}
	case customerID == uuid.Nil:
		return Order{}, InvalidOrderError{Reason: "User-Id: обязательный заголовок"}
	case restaurantID == uuid.Nil:
		return Order{}, InvalidOrderError{Reason: "restaurant_id: обязательное поле"}
	case utf8.RuneCountInString(address) < 1 || utf8.RuneCountInString(address) > addressMaxLength:
		return Order{}, InvalidOrderError{
			Reason: fmt.Sprintf("адрес: от 1 до %d знаков", addressMaxLength),
		}
	case len(items) < 1 || len(items) > orderItemsMax:
		return Order{}, InvalidOrderError{
			Reason: fmt.Sprintf("состав заказа: от 1 до %d позиций", orderItemsMax),
		}
	}

	total, err := totalOf(items)
	if err != nil {
		return Order{}, err
	}

	return Order{
		ID:           id,
		CustomerID:   customerID,
		RestaurantID: restaurantID,
		Status:       Pending,
		Address:      address,
		Items:        slices.Clone(items),
		Total:        total,
		RequestHash:  requestHash(restaurantID, address, items),
		CreatedAt:    at,
		UpdatedAt:    at,
	}, nil
}

// totalOf проверяет состав заказа и считает его сумму.
//
// Одна и та же позиция дважды в составе — сломанный запрос, а не заказ двух порций:
// заведение проверяет наличие по каждой строке отдельно и приняло бы заказ, которому
// вместе не хватает остатка. Корзина складывает одинаковые позиции на клиенте.
func totalOf(items []OrderItem) (Money, error) {
	total := Money(0)
	seen := make(map[uuid.UUID]struct{}, len(items))

	for _, item := range items {
		switch {
		case item.ProductID == uuid.Nil:
			return 0, InvalidOrderError{Reason: "product_id: обязательное поле позиции"}
		case item.Quantity < 1 || item.Quantity > itemQuantityMax:
			return 0, InvalidOrderError{
				Reason: fmt.Sprintf("количество: от 1 до %d единиц", itemQuantityMax),
			}
		case item.Price < 0:
			return 0, InvalidOrderError{Reason: "цена позиции: не может быть отрицательной"}
		}

		if _, duplicate := seen[item.ProductID]; duplicate {
			return 0, InvalidOrderError{
				Reason: "состав заказа: товар " + item.ProductID.String() + " встречается дважды",
			}
		}
		seen[item.ProductID] = struct{}{}

		total += item.Cost()
	}

	return total, nil
}

// TransitionTo переводит заказ в состояние status по требованию actor.
//
// Это единственный способ изменить состояние заказа: правило живёт в матрице, а не в том,
// кто её вызвал, — обработчик уведомления заведения и админская отмена спрашивают одну и
// ту же таблицу и не могут разойтись в понимании того, что заказу разрешено.
func (o *Order) TransitionTo(status OrderStatus, by Actor, at time.Time) error {
	if !slices.Contains(transitions[o.Status][status], by) {
		return OrderTransitionError{From: o.Status, To: status, By: by}
	}

	o.Status = status
	o.UpdatedAt = at

	return nil
}

// Cancel отменяет заказ по требованию актора и запоминает, кто и почему его отменил.
//
// Причина обязательна всем, кроме покупателя: её показывают покупателю, и «заказ отменён»
// без объяснения — это обращение в поддержку, а не ответ. Покупателю объяснять свою же
// отмену некому.
func (o *Order) Cancel(by Actor, reason string, at time.Time) error {
	// Пробелы по краям — след ввода, а не часть причины: причина «  » причиной не является.
	reason = strings.TrimSpace(reason)

	switch {
	case by != ByCustomer && reason == "":
		return InvalidOrderError{Reason: "причина отмены: обязательна для заведения и саппорта"}
	case utf8.RuneCountInString(reason) > reasonMaxLength:
		return InvalidOrderError{
			Reason: fmt.Sprintf("причина отмены: не длиннее %d знаков", reasonMaxLength),
		}
	}

	if err := o.TransitionTo(Cancelled, by, at); err != nil {
		return err
	}

	o.CancelledBy = by
	o.CancellationReason = reason

	return nil
}

// OrderTransitionError — перехода, которого просят, матрица не допускает: из этого
// состояния в это либо не переходят вовсе, либо переходят, но не по требованию этого
// инициатора.
//
// Ошибка несёт все три составляющие правила: по ней вызывающий выбирает ответ — покупателю
// на попытку отменить готовящийся заказ отвечают отдельным кодом причины, а опоздавшее
// уведомление заведения просто отбрасывается.
type OrderTransitionError struct {
	// From — состояние, в котором заказ находится сейчас.
	From OrderStatus
	// To — состояние, в которое просят его перевести.
	To OrderStatus
	// By — кто этого требует.
	By Actor
}

// Error возвращает причину, по которой переход не выполнен.
func (e OrderTransitionError) Error() string {
	return fmt.Sprintf("переход %s → %s по требованию %s не разрешён", e.From, e.To, e.By)
}

// InvalidOrderError — заказ описали так, что принять его нельзя ни при каком состоянии
// каталога. Причина — текстом: её читает разработчик клиента, который в состоянии
// исправить запрос, а ветки поведения у сломанного запроса нет.
type InvalidOrderError struct {
	// Reason — что именно не так.
	Reason string
}

// Error возвращает причину отказа.
func (e InvalidOrderError) Error() string {
	return e.Reason
}

// ErrOrderNotFound — заказа с таким идентификатором у этого покупателя нет.
//
// Одна ошибка на «заказа не существует» и «заказ чужой» намеренно: по разным ответам
// перебор идентификаторов рассказал бы, какие заказы на площадке есть, а покупателю обе
// ситуации одинаковы — этого заказа у него нет.
var ErrOrderNotFound = errors.New("order not found")
