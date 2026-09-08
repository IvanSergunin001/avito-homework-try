package http_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// incomingOrderItem — позиция заказа глазами оператора: он видит, что готовить и сколько.
type incomingOrderItem struct {
	MenuItemID string `json:"menu_item_id"`
	Name       string `json:"name"`
	Price      int64  `json:"price"`
	Quantity   int    `json:"quantity"`
}

// operatorOrder — входящий заказ на рабочем месте оператора.
type operatorOrder struct {
	ID                 string              `json:"id"`
	PlatformOrderID    string              `json:"platform_order_id"`
	Status             string              `json:"status"`
	Address            string              `json:"address"`
	Items              []incomingOrderItem `json:"items"`
	Total              int64               `json:"total"`
	CancellationReason string              `json:"cancellation_reason"`
}

// operatorOrderPage — страница входящих заказов.
type operatorOrderPage struct {
	Items []operatorOrder `json:"items"`
	Page  pageMeta        `json:"page"`
}

// incomingOrders читает страницу входящих заказов на рабочем месте оператора.
func (b *box) incomingOrders(t *testing.T, query string) operatorOrderPage {
	t.Helper()

	var page operatorOrderPage
	decode(t, b.get(t, operatorOrders+query), http.StatusOK, &page)

	return page
}

// onlyIncomingOrder возвращает единственный заказ коробки: тесты рассмотрения заводят
// ровно один, и выбирать из списка им нечего.
func (b *box) onlyIncomingOrder(t *testing.T) operatorOrder {
	t.Helper()

	page := b.incomingOrders(t, "")
	if len(page.Items) != 1 {
		t.Fatalf("у оператора %d заказов, ожидали один", len(page.Items))
	}

	return page.Items[0]
}

// confirm — оператор берётся за заказ.
func (b *box) confirm(t *testing.T, id string) response {
	t.Helper()

	return b.send(t, http.MethodPost, operatorOrders+"/"+id+"/confirm", nil)
}

// reject — оператор отказывается от заказа, называя причину.
func (b *box) reject(t *testing.T, id, reason string) response {
	t.Helper()

	return b.send(t, http.MethodPost, operatorOrders+"/"+id+"/reject",
		map[string]string{"reason": reason})
}

// acceptedOrder заводит позицию, передаёт коробке заказ на неё и возвращает заказ таким,
// каким его видит оператор.
func acceptedOrder(t *testing.T, b *box, quantity, stock int) operatorOrder {
	t.Helper()

	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: stock})
	if resp := b.place(t, orderFor(item, quantity)); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	return b.onlyIncomingOrder(t)
}

func TestOperatorSeesIncomingOrderWithItsItemsAndAddress(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	incoming := b.onlyIncomingOrder(t)

	if incoming.Address != address {
		t.Errorf("адрес доставки %q, ожидали %q: везти заказ иначе некуда", incoming.Address, address)
	}
	if len(incoming.Items) != 1 {
		t.Fatalf("в заказе %d позиций, ожидали одну", len(incoming.Items))
	}
	if incoming.Items[0].Name != "Круассан с миндалём" || incoming.Items[0].Quantity != 2 {
		t.Errorf("оператор видит %q в количестве %d, ожидали 2 круассана: по этому он и готовит",
			incoming.Items[0].Name, incoming.Items[0].Quantity)
	}
	if incoming.Total != 90000 {
		t.Errorf("сумма заказа %d, ожидали 90000", incoming.Total)
	}
	if incoming.Status != "under_review" {
		t.Errorf("состояние заказа %q, ожидали under_review: решение за оператором", incoming.Status)
	}
}

func TestIncomingOrderKeepsTheLinkToTheOrderOnThePlatform(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 1)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	incoming := b.onlyIncomingOrder(t)

	if incoming.PlatformOrderID != order.OrderID {
		t.Errorf("заказ ссылается на %q, а площадка передавала %q: разбираться с ней не с чем",
			incoming.PlatformOrderID, order.OrderID)
	}
	if incoming.ID == order.OrderID {
		t.Error("заказ коробки взял идентификатор заказа площадки: у коробки свой заказ и свой номер")
	}
}

func TestOperatorConfirmingTheOrderPutsItInWork(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 2, 5)

	var confirmed operatorOrder
	decode(t, b.confirm(t, incoming.ID), http.StatusOK, &confirmed)

	if confirmed.Status != "confirmed" {
		t.Errorf("состояние заказа %q, ожидали confirmed", confirmed.Status)
	}
	if b.onlyIncomingOrder(t).Status != "confirmed" {
		t.Error("в списке заказ остался прежним: оператор увидел бы своё решение только после перезапуска")
	}
}

func TestConfirmedOrderKeepsWhatItTookFromStock(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 2, 5)

	if resp := b.confirm(t, incoming.ID); resp.Status != http.StatusOK {
		t.Fatalf("заказ не подтверждён: %d %s", resp.Status, resp.Body)
	}

	if left := b.stockOf(t, incoming.Items[0].MenuItemID); left != 3 {
		t.Errorf("остаток %d, ожидали 3: заказ в работе потратит списанное", left)
	}
}

func TestOperatorRejectsTheOrderAtReviewWithAReasonForTheCustomer(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 2, 5)

	var rejected operatorOrder
	decode(t, b.reject(t, incoming.ID, "Закончились продукты"), http.StatusOK, &rejected)

	if rejected.Status != "cancelled" {
		t.Errorf("состояние отклонённого заказа %q, ожидали cancelled", rejected.Status)
	}
	if rejected.CancellationReason != "Закончились продукты" {
		t.Errorf("причина отказа %q, ожидали названную оператором: её увидит покупатель",
			rejected.CancellationReason)
	}
}

func TestRejectedOrderReturnsWhatItTookToTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 2, 5)

	if resp := b.reject(t, incoming.ID, "Закончились продукты"); resp.Status != http.StatusOK {
		t.Fatalf("заказ не отклонён: %d %s", resp.Status, resp.Body)
	}

	if left := b.stockOf(t, incoming.Items[0].MenuItemID); left != 5 {
		t.Errorf("остаток %d, ожидали вернувшиеся 5: продукты на отклонённый заказ не потрачены", left)
	}
}

func TestOrderIsDecidedOnlyOnceAtReview(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 1, 5)

	if resp := b.confirm(t, incoming.ID); resp.Status != http.StatusOK {
		t.Fatalf("заказ не подтверждён: %d %s", resp.Status, resp.Body)
	}

	refusal := asRefusal(t, b.reject(t, incoming.ID, "Закончились продукты"), http.StatusConflict)
	if refusal.Code != "order_not_under_review" {
		t.Errorf("причина отказа %q, ожидали order_not_under_review", refusal.Code)
	}
	if b.onlyIncomingOrder(t).Status != "confirmed" {
		t.Error("состояние заказа изменилось, а решение по нему было принято раньше")
	}
}

func TestOrderIsNotRejectedWithoutAReason(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 1, 5)

	refusal := asRefusal(t, b.reject(t, incoming.ID, "   "), http.StatusBadRequest)
	if refusal.Code != "validation_failed" {
		t.Errorf("причина отказа %q, ожидали validation_failed", refusal.Code)
	}
	if b.onlyIncomingOrder(t).Status != "under_review" {
		t.Error("заказ отклонён без причины, а покупателю нечего показать")
	}
}

func TestDecisionOnAnOrderTheBoxNeverAcceptedIsNotFound(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	refusal := asRefusal(t, b.confirm(t, uuid.NewString()), http.StatusNotFound)
	if refusal.Code != "order_not_found" {
		t.Errorf("причина отказа %q, ожидали order_not_found", refusal.Code)
	}
}

func TestEmptyOrderListIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	t.Parallel()

	if page := newBox(t).incomingOrders(t, ""); page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: поверхность обещает коллекцию всегда")
	}
}
