package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// addressMaxLength, orderItemsMax, itemQuantityMax и reasonMaxLength — пределы,
	// объявленные партнёрским контрактом и поверхностью оператора. Считаем длины в
	// знаках, а не в байтах: адрес пишут кириллицей, и байтовый предел обрезал бы
	// московский адрес вдвое раньше обещанного.
	addressMaxLength = 500
	orderItemsMax    = 100
	itemQuantityMax  = 100
	reasonMaxLength  = 500
)

// OrderStatus — состояние заказа в коробке.
//
// Названия совпадают с теми, которыми заказ живёт на площадке: коробка сообщает
// площадке о своих переходах, и два словаря для одних и тех же состояний означали бы
// перевод на каждой границе — и расхождение при первой же правке одного из них.
type OrderStatus string

const (
	// UnderReview — заказ принят по контракту и ждёт решения оператора.
	UnderReview OrderStatus = "under_review"
	// Confirmed — оператор взялся за заказ.
	Confirmed OrderStatus = "confirmed"
	// Cancelled — оператор отклонил заказ на этапе рассмотрения.
	Cancelled OrderStatus = "cancelled"
)

// RequestedItem — позиция заказа так, как её передала площадка: чем заказали, сколько и
// по какой цене её показали покупателю.
type RequestedItem struct {
	// ExternalID — идентификатор позиции в системе заведения, каким его знает площадка.
	ExternalID string
	// Quantity — сколько единиц позиции заказали.
	Quantity int
	// Price — цена, по которой площадка показала позицию покупателю.
	Price Money
}

// OrderRequest — заказ, переданный площадкой по партнёрскому контракту.
//
// Это ещё не заказ коробки, а просьба его принять: коробка — источник истины по наличию
// и цене, и что из просьбы получится, решает предметная область, а не площадка.
type OrderRequest struct {
	// PlatformOrderID — идентификатор заказа на площадке.
	PlatformOrderID uuid.UUID
	// IdempotencyKey — ключ, по которому коробка узнаёт повторную отправку того же заказа.
	IdempotencyKey uuid.UUID
	// Address — адрес доставки в свободной форме.
	Address string
	// Items — состав заказа.
	Items []RequestedItem
	// Total — сумма, посчитанная площадкой; коробка сверяет её со своим расчётом.
	Total Money
	// PlacedAt — когда покупатель оформил заказ; площадка может его не прислать.
	PlacedAt time.Time
}

// NewOrderRequest собирает просьбу принять заказ, проверяя то, что прислала площадка.
//
// Пределы объявлены контрактом, но кодогенератор их не сверяет, а правило «у заказа есть
// адрес и непустой состав» одинаково верно, откуда бы заказ ни пришёл. Проверка здесь
// отделена от решения о приёме: заказ, в котором нет адреса, — это сломанный вызов, а не
// заказ, который заведение не может выполнить.
func NewOrderRequest(
	platformOrderID, idempotencyKey uuid.UUID,
	address string, items []RequestedItem, total Money, placedAt time.Time,
) (OrderRequest, error) {
	address = strings.TrimSpace(address)

	switch {
	case platformOrderID == uuid.Nil:
		return OrderRequest{}, InvalidOrderError{Reason: "order_id: обязательное поле"}
	case idempotencyKey == uuid.Nil:
		return OrderRequest{}, InvalidOrderError{Reason: "Idempotency-Key: обязательный заголовок"}
	case utf8.RuneCountInString(address) < 1 || utf8.RuneCountInString(address) > addressMaxLength:
		return OrderRequest{}, InvalidOrderError{
			Reason: fmt.Sprintf("адрес: от 1 до %d знаков", addressMaxLength),
		}
	case len(items) < 1 || len(items) > orderItemsMax:
		return OrderRequest{}, InvalidOrderError{
			Reason: fmt.Sprintf("состав заказа: от 1 до %d позиций", orderItemsMax),
		}
	case total < 0:
		return OrderRequest{}, InvalidOrderError{Reason: "сумма заказа: не может быть отрицательной"}
	}

	// Одна и та же позиция дважды в составе — сломанный вызов, а не заказ двух порций:
	// иначе проверка остатка шла бы по каждой строке отдельно и пропустила бы заказ,
	// которому вместе не хватает. Площадка складывает одинаковые позиции в корзине сама.
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		switch {
		case strings.TrimSpace(item.ExternalID) == "":
			return OrderRequest{}, InvalidOrderError{Reason: "external_id: обязательное поле позиции"}
		case item.Quantity < 1 || item.Quantity > itemQuantityMax:
			return OrderRequest{}, InvalidOrderError{
				Reason: fmt.Sprintf("количество: от 1 до %d единиц", itemQuantityMax),
			}
		case item.Price < 0:
			return OrderRequest{}, InvalidOrderError{Reason: "цена позиции: не может быть отрицательной"}
		}

		if _, duplicate := seen[item.ExternalID]; duplicate {
			return OrderRequest{}, InvalidOrderError{
				Reason: "состав заказа: позиция " + item.ExternalID + " встречается дважды",
			}
		}
		seen[item.ExternalID] = struct{}{}
	}

	return OrderRequest{
		PlatformOrderID: platformOrderID,
		IdempotencyKey:  idempotencyKey,
		Address:         address,
		Items:           items,
		Total:           total,
		PlacedAt:        placedAt,
	}, nil
}

// ExternalIDs перечисляет позиции меню, которые нужны, чтобы решить судьбу заказа.
func (r OrderRequest) ExternalIDs() []string {
	ids := make([]string, 0, len(r.Items))
	for _, item := range r.Items {
		ids = append(ids, item.ExternalID)
	}

	return ids
}

// OrderItem — позиция принятого заказа: снимок на момент приёма.
//
// Название и цена хранятся копией, а не ссылкой на позицию меню: заказ не должен меняться
// задним числом, когда оператор правит меню, — иначе в чеке оказалась бы цена, которой
// покупатель не видел, а у удалённой позиции пропало бы и название.
type OrderItem struct {
	// MenuItemID — позиция меню, из которой собран заказ.
	MenuItemID uuid.UUID
	// Name — название позиции на момент приёма заказа.
	Name string
	// Price — цена позиции на момент приёма заказа.
	Price Money
	// Quantity — сколько единиц заказали.
	Quantity int
}

// Cost — сколько стоит эта строка заказа.
func (i OrderItem) Cost() Money {
	return i.Price * Money(i.Quantity)
}

// Order — заказ в коробке: корень агрегата. Позиции живут внутри него и отдельной жизни
// не имеют, а переходы между состояниями — методы заказа, а не правила обработчиков.
type Order struct {
	// ID — идентификатор заказа в коробке.
	ID uuid.UUID
	// PlatformOrderID — тот же заказ на площадке. Коробка хранит ссылку на него, чтобы
	// было с чем прийти к площадке, когда заказ вызывает вопросы.
	PlatformOrderID uuid.UUID
	// IdempotencyKey — ключ, по которому узнаётся повторная отправка этого заказа.
	// По контракту он совпадает с идентификатором заказа на площадке, но хранится
	// отдельно: обе величины даёт площадка, и коробке не о чем судить, если однажды они
	// разойдутся, — дедупликация идёт по ключу, а ссылка на заказ остаётся ссылкой.
	IdempotencyKey uuid.UUID
	// Status — состояние заказа.
	Status OrderStatus
	// Address — адрес доставки.
	Address string
	// Items — состав заказа.
	Items []OrderItem
	// Total — сумма заказа, посчитанная коробкой по своим ценам.
	Total Money
	// CancellationReason — почему оператор отклонил заказ; у остальных заказов пуста.
	CancellationReason string
	// PlacedAt — когда покупатель оформил заказ; может быть нулевым, если площадка не
	// прислала времени.
	PlacedAt time.Time
	// AcceptedAt — когда коробка приняла заказ.
	AcceptedAt time.Time
}

// AcceptOrder решает, берётся ли коробка за переданный заказ, и собирает его.
//
// Коробка — источник истины по наличию и цене: любое расхождение с тем, что показала
// площадка, — это отказ с машиночитаемой причиной, а не молчаливое исправление. Приняв
// заказ, коробка обязуется его выполнить, поэтому проверка идёт до того, как остаток
// списан, и по тому меню, которое действует в этот момент.
//
// menu — позиции, о которых спрашивает заказ, по их внешним идентификаторам; позиции,
// которой в меню нет, в нём просто нет ключа.
func AcceptOrder(
	id uuid.UUID, request OrderRequest, menu map[string]MenuItem, hours WorkingHours, at time.Time,
) (Order, error) {
	// Закрытое заведение отказывает до разбора состава: когда готовить некому, неважно,
	// что именно заказали, — и рассказывать площадке о наличии в такой момент незачем.
	if !hours.IsOpenAt(at) {
		return Order{}, OrderRefusedError{
			Reason: RestaurantClosed,
			Detail: "заведение сейчас закрыто: часы работы " + hours.String(),
		}
	}

	items := make([]OrderItem, 0, len(request.Items))
	total := Money(0)

	for _, requested := range request.Items {
		item, known := menu[requested.ExternalID]
		if !known {
			return Order{}, OrderRefusedError{
				Reason:            UnknownProduct,
				ProductExternalID: requested.ExternalID,
				Detail:            "такой позиции в меню заведения нет",
			}
		}

		if err := availableFor(item, requested); err != nil {
			return Order{}, err
		}

		line := OrderItem{
			MenuItemID: item.ID,
			Name:       item.Name,
			Price:      item.Price,
			Quantity:   requested.Quantity,
		}

		items = append(items, line)
		total += line.Cost()
	}

	// Сумма пересчитана по ценам заведения, и цены уже сошлись позиция в позицию —
	// значит, расхождение в итоге означает, что площадка посчитала не то, что прислала.
	// Это сломанный вызов, а не отказ по наличию: причины «мы не можем это выполнить»
	// здесь нет.
	if total != request.Total {
		return Order{}, InvalidOrderError{
			Reason: fmt.Sprintf("сумма заказа: прислана %d, по составу выходит %d", request.Total, total),
		}
	}

	return Order{
		ID:              id,
		PlatformOrderID: request.PlatformOrderID,
		IdempotencyKey:  request.IdempotencyKey,
		Status:          UnderReview,
		Address:         request.Address,
		Items:           items,
		Total:           total,
		PlacedAt:        request.PlacedAt,
		AcceptedAt:      at,
	}, nil
}

// availableFor проверяет, может ли заведение отдать заказанное количество этой позиции
// по той цене, которую площадка показала покупателю.
//
// Стоп-лист и нехватка остатка отвечают одним кодом `out_of_stock`: перечень кодов
// контракта закрыт, и площадке эти случаи не различать — заказать позицию сейчас нельзя
// в обоих. Чем именно она недоступна, видно человеку в `detail`.
func availableFor(item MenuItem, requested RequestedItem) error {
	switch {
	case item.InStopList:
		return OrderRefusedError{
			Reason:            OutOfStock,
			ProductExternalID: requested.ExternalID,
			Detail:            "позиция в стоп-листе",
		}
	case int(item.Stock) < requested.Quantity:
		return OrderRefusedError{
			Reason:            OutOfStock,
			ProductExternalID: requested.ExternalID,
			Detail: fmt.Sprintf("заказано %d, в наличии %d",
				requested.Quantity, int(item.Stock)),
		}
	case item.Price != requested.Price:
		return OrderRefusedError{
			Reason:            PriceChanged,
			ProductExternalID: requested.ExternalID,
			CurrentPrice:      item.Price,
			Detail: fmt.Sprintf("цена изменилась: площадка показала %d, у заведения %d",
				requested.Price, item.Price),
		}
	}

	return nil
}

// Confirm — оператор берётся за заказ.
//
// Подтвердить можно только заказ на рассмотрении: подтверждение уже подтверждённого
// заказа ничего не значит, а подтверждение отклонённого вернуло бы в работу то, от чего
// заведение отказалось, — и покупатель узнал бы об этом после отмены.
func (o *Order) Confirm() error {
	if o.Status != UnderReview {
		return ErrOrderNotUnderReview
	}

	o.Status = Confirmed

	return nil
}

// Reject — заведение не берётся за заказ.
//
// Причина обязательна: её площадка показывает покупателю, и «заказ отменён» без
// объяснения — это обращение в поддержку, а не ответ. Отклонить можно только заказ на
// рассмотрении: подтверждённый заказ уже в работе, и отменяют его иначе.
func (o *Order) Reject(reason string) error {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > reasonMaxLength {
		return InvalidOrderError{
			Reason: fmt.Sprintf("причина отказа: от 1 до %d знаков", reasonMaxLength),
		}
	}

	if o.Status != UnderReview {
		return ErrOrderNotUnderReview
	}

	o.Status = Cancelled
	o.CancellationReason = reason

	return nil
}

// RefusalReason — машиночитаемая причина, по которой коробка не берёт заказ. Значения
// совпадают с кодами партнёрского контракта: перечень там закрыт, и заводить рядом с ним
// второй словарь означало бы переводить одно в другое на границе.
type RefusalReason string

const (
	// RestaurantClosed — заведение сейчас не работает.
	RestaurantClosed RefusalReason = "restaurant_closed"
	// UnknownProduct — такой позиции в меню заведения нет.
	UnknownProduct RefusalReason = "unknown_product"
	// OutOfStock — позиция в стоп-листе либо её осталось меньше, чем заказали.
	OutOfStock RefusalReason = "out_of_stock"
	// PriceChanged — цена заведения отличается от той, что показала площадка.
	PriceChanged RefusalReason = "price_changed"
)

// OrderRefusedError — коробка не берёт заказ, и вот почему.
//
// Причина машиночитаемая: по ней площадка выбирает ветку поведения — обновить каталог,
// показать покупателю новую цену или убрать позицию с витрины, — а текст `Detail`
// предназначен человеку, который будет разбираться с конкретным случаем.
type OrderRefusedError struct {
	// Reason — машиночитаемая причина отказа.
	Reason RefusalReason
	// ProductExternalID — позиция, из-за которой отказан заказ; у отказа из-за закрытого
	// заведения его нет.
	ProductExternalID string
	// CurrentPrice — актуальная цена позиции; заполнена при причине PriceChanged.
	CurrentPrice Money
	// Detail — пояснение для человека.
	Detail string
}

// Error возвращает причину отказа.
func (e OrderRefusedError) Error() string {
	if e.ProductExternalID == "" {
		return fmt.Sprintf("заказ не принят (%s): %s", e.Reason, e.Detail)
	}

	return fmt.Sprintf("заказ не принят (%s), позиция %s: %s", e.Reason, e.ProductExternalID, e.Detail)
}

// InvalidOrderError — заказ передали так, что принять его нельзя ни при каком состоянии
// меню. Причина — текстом: её читает разработчик по ту сторону контракта, а ветки
// поведения у сломанного вызова нет.
type InvalidOrderError struct {
	// Reason — что именно не так.
	Reason string
}

// Error возвращает причину отказа.
func (e InvalidOrderError) Error() string {
	return e.Reason
}

// ErrOrderNotFound — заказа с таким идентификатором в коробке нет.
var ErrOrderNotFound = errors.New("order not found")

// ErrOrderNotUnderReview — заказ уже не на рассмотрении: решение по нему принято раньше.
var ErrOrderNotUnderReview = errors.New("order is not under review")

// ErrOrderAlreadyAccepted — заказ с этим ключом идемпотентности коробка уже приняла.
// Второй заказ не заводится: повтор получает результат первой попытки.
var ErrOrderAlreadyAccepted = errors.New("order with this idempotency key is already accepted")
