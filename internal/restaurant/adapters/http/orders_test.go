package http_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// address — адрес доставки, который площадка передаёт вместе с заказом.
const address = "Москва, Лесная 7, кв. 12"

// incomingItem — позиция заказа в схеме партнёрского контракта: коробка узнаёт позицию по
// внешнему идентификатору и сверяет цену, которую площадка показала покупателю.
type incomingItem struct {
	ExternalID string `json:"external_id"`
	Quantity   int    `json:"quantity"`
	Price      int64  `json:"price"`
}

// incomingOrder — заказ, каким его передаёт площадка по партнёрскому контракту.
type incomingOrder struct {
	OrderID string         `json:"order_id"`
	Address string         `json:"address"`
	Items   []incomingItem `json:"items"`
	Total   int64          `json:"total"`
}

// acceptance — подтверждение приёма заказа в схеме контракта.
type acceptance struct {
	OrderID    string    `json:"order_id"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// orderFor собирает заказ на позицию меню по той цене, которую коробка отдала площадке.
func orderFor(item operatorItem, quantity int) incomingOrder {
	return incomingOrder{
		OrderID: uuid.NewString(),
		Address: address,
		Items:   []incomingItem{{ExternalID: item.ID, Quantity: quantity, Price: item.Price}},
		Total:   item.Price * int64(quantity),
	}
}

// deliver передаёт заказ коробке так, как это делает площадка: с ключом идемпотентности и
// номером попытки в заголовках контракта.
func (b *box) deliver(t *testing.T, order incomingOrder, key string, attempt int) response {
	t.Helper()

	headers := http.Header{}
	headers.Set("Idempotency-Key", key)
	headers.Set("Idempotency-Attempt", strconv.Itoa(attempt))

	return b.sendWithHeaders(t, http.MethodPost, contractOrders, order, headers)
}

// place передаёт заказ первой попыткой — ключ идемпотентности равен идентификатору заказа
// площадки, как того требует контракт.
func (b *box) place(t *testing.T, order incomingOrder) response {
	t.Helper()

	return b.deliver(t, order, order.OrderID, 1)
}

// stockOf читает остаток позиции глазами оператора.
func (b *box) stockOf(t *testing.T, id string) int {
	t.Helper()

	for _, item := range b.operatorMenuPage(t, "").Items {
		if item.ID == id {
			return item.Stock
		}
	}

	t.Fatalf("позиции %s нет в меню", id)

	return 0
}

func TestBoxAcceptsAnOrderPassedByTheContract(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	var accepted acceptance
	decode(t, b.place(t, order), http.StatusCreated, &accepted)

	if accepted.OrderID != order.OrderID {
		t.Errorf("коробка подтвердила заказ %s, а площадка передавала %s: сверять ответ ей не с чем",
			accepted.OrderID, order.OrderID)
	}
	if accepted.AcceptedAt.IsZero() {
		t.Error("в подтверждении нет времени приёма")
	}
}

func TestAcceptedOrderTakesWhatItOrderedFromStock(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})

	if resp := b.place(t, orderFor(item, 2)); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	if left := b.stockOf(t, item.ID); left != 3 {
		t.Errorf("остаток %d, ожидали 3: принятый заказ списывает остаток", left)
	}
}

func TestOrderForAnItemTheBoxDoesNotKnowIsRefusedAsUnknownProduct(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := incomingOrder{
		OrderID: uuid.NewString(),
		Address: address,
		Items:   []incomingItem{{ExternalID: uuid.NewString(), Quantity: 1, Price: 45000}},
		Total:   45000,
	}

	refusal := asRefusal(t, b.place(t, order), http.StatusConflict)
	if refusal.Code != "unknown_product" {
		t.Errorf("причина отказа %q, ожидали unknown_product", refusal.Code)
	}
	if refusal.ProductExternalID != order.Items[0].ExternalID {
		t.Errorf("отказ называет позицию %q, ожидали %q: площадке нужно знать, какую чинить",
			refusal.ProductExternalID, order.Items[0].ExternalID)
	}
}

func TestOrderForAnItemInTheStopListIsRefusedAsOutOfStock(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	b.markStopList(t, item.ID, true)

	refusal := asRefusal(t, b.place(t, orderFor(item, 1)), http.StatusConflict)
	if refusal.Code != "out_of_stock" {
		t.Errorf("причина отказа %q, ожидали out_of_stock", refusal.Code)
	}
	if refusal.ProductExternalID != item.ID {
		t.Errorf("отказ называет позицию %q, ожидали %q", refusal.ProductExternalID, item.ID)
	}
}

func TestOrderForMoreThanIsLeftIsRefusedAndTakesNothingFromStock(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 2})

	refusal := asRefusal(t, b.place(t, orderFor(item, 3)), http.StatusConflict)
	if refusal.Code != "out_of_stock" {
		t.Errorf("причина отказа %q, ожидали out_of_stock", refusal.Code)
	}
	if left := b.stockOf(t, item.ID); left != 2 {
		t.Errorf("остаток %d, ожидали нетронутые 2: непринятый заказ ничего не списывает", left)
	}
}

func TestPriceDifferentFromTheOnePlatformSentIsRefusedWithTheCurrentPrice(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 49000, Stock: 5})

	// Площадка показала покупателю старую цену: у неё в каталоге меню прошлой синхронизации.
	stale := orderFor(item, 1)
	stale.Items[0].Price = 45000
	stale.Total = 45000

	refusal := asRefusal(t, b.place(t, stale), http.StatusConflict)
	if refusal.Code != "price_changed" {
		t.Errorf("причина отказа %q, ожидали price_changed", refusal.Code)
	}
	if refusal.CurrentPrice == nil || *refusal.CurrentPrice != 49000 {
		t.Errorf("отказ не назвал действующую цену (%v), а коробка — источник истины по цене",
			refusal.CurrentPrice)
	}
}

func TestClosedRestaurantRefusesTheOrder(t *testing.T) {
	t.Parallel()

	// Часы работы отсчитываются от текущего времени: заведение откроется через час, и
	// заказ приходит в закрытое. Прогон в любое время суток означает одно и то же.
	opens := time.Now().Add(time.Hour).Format("15:04")
	closes := time.Now().Add(2 * time.Hour).Format("15:04")

	hours, err := domain.NewWorkingHours(opens, closes)
	if err != nil {
		t.Fatalf("задать часы работы: %v", err)
	}

	b := openBox(t, hours)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})

	refusal := asRefusal(t, b.place(t, orderFor(item, 1)), http.StatusConflict)
	if refusal.Code != "restaurant_closed" {
		t.Errorf("причина отказа %q, ожидали restaurant_closed", refusal.Code)
	}
}

func TestSameOrderDeliveredTwiceIsNotPreparedTwice(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	var first acceptance
	decode(t, b.deliver(t, order, order.OrderID, 1), http.StatusCreated, &first)

	// Ответ первой попытки до площадки не доехал, и она повторяет доставку: тот же ключ,
	// следующий номер попытки.
	var second acceptance
	decode(t, b.deliver(t, order, order.OrderID, 2), http.StatusOK, &second)

	if second.OrderID != first.OrderID || !second.AcceptedAt.Equal(first.AcceptedAt) {
		t.Errorf("повтор получил другой ответ (%+v против %+v), ожидали результат первой попытки",
			second, first)
	}
	if left := b.stockOf(t, item.ID); left != 3 {
		t.Errorf("остаток %d, ожидали 3: повтор не списывает остаток второй раз", left)
	}

	if incoming := b.incomingOrders(t, ""); incoming.Page.Total != 1 {
		t.Errorf("у оператора %d заказов, ожидали один: повторная отправка не заводит второй",
			incoming.Page.Total)
	}
}

func TestOrderDeliveredWithoutIdempotencyKeyIsNotAccepted(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})

	// Заголовки контракта не приложены: узнать повтор такого заказа коробке нечем.
	resp := b.send(t, http.MethodPost, contractOrders, orderFor(item, 1))

	if refusal := asRefusal(t, resp, http.StatusBadRequest); refusal.Code != "validation_failed" {
		t.Errorf("причина отказа %q, ожидали validation_failed", refusal.Code)
	}
}

func TestOrderWithoutDeliveryAddressIsNotAccepted(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})

	order := orderFor(item, 1)
	order.Address = ""

	if refusal := asRefusal(t, b.place(t, order), http.StatusBadRequest); refusal.Code != "validation_failed" {
		t.Errorf("причина отказа %q, ожидали validation_failed", refusal.Code)
	}
	if left := b.stockOf(t, item.ID); left != 5 {
		t.Errorf("остаток %d, ожидали нетронутые 5", left)
	}
}
