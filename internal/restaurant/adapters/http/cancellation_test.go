// Отмена принятого заказа проверяется через тот же шов коробки: настоящий роутер со всеми
// middleware, настоящий PostgreSQL, а площадка по ту сторону — обычный HTTP-вызов
// партнёрского контракта, каким его делает она сама.
//
// Заказ отменяется на партнёрской поверхности, а результат тест читает на рабочем месте
// оператора — там, где его увидит человек на кухне: заказ один, и посмотреть на него
// глазами обеих сторон можно только пройдя обе двери.
package http_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// cancellation — подтверждение отмены в схеме партнёрского контракта.
type cancellation struct {
	OrderID     string    `json:"order_id"`
	CancelledAt time.Time `json:"cancelled_at"`
}

// cancel просит коробку отменить заказ так, как это делает площадка: по идентификатору
// заказа площадки и с причиной для оператора.
func (b *box) cancel(t *testing.T, platformOrderID, reason string) response {
	t.Helper()

	return b.send(t, http.MethodPost, contractOrders+"/"+platformOrderID+"/cancel",
		map[string]string{"reason": reason})
}

func TestOrderCancelledByThePlatformLeavesTheKitchenAndReturnsItsProductsToTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	var cancelled cancellation
	decode(t, b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"), http.StatusOK, &cancelled)

	if cancelled.OrderID != order.OrderID {
		t.Errorf("коробка подтвердила отмену заказа %s, а площадка отменяла %s: сверять ответ ей не с чем",
			cancelled.OrderID, order.OrderID)
	}
	if cancelled.CancelledAt.IsZero() {
		t.Error("в подтверждении нет времени отмены")
	}

	incoming := b.onlyIncomingOrder(t)
	if incoming.Status != "cancelled" {
		t.Errorf("оператор видит заказ в состоянии %q, ожидали \"cancelled\": заказ у него забрали",
			incoming.Status)
	}
	if incoming.CancellationReason != "Отмена по просьбе покупателя" {
		t.Errorf("причина отмены у оператора %q: «отменён» без единого слова читается как сбой интеграции",
			incoming.CancellationReason)
	}

	if left := b.stockOf(t, item.ID); left != 5 {
		t.Errorf("остаток %d, ожидали 5: продукты на отменённый заказ не потрачены и снова продаются", left)
	}
}

func TestOrderTheKitchenAlreadyStartedIsNotCancelledThroughThePartnerContract(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	incoming := b.onlyIncomingOrder(t)
	if resp := b.confirm(t, incoming.ID); resp.Status != http.StatusOK {
		t.Fatalf("оператор не взялся за заказ: %d %s", resp.Status, resp.Body)
	}
	if resp := b.startCooking(t, incoming.ID); resp.Status != http.StatusOK {
		t.Fatalf("заказ не ушёл в готовку: %d %s", resp.Status, resp.Body)
	}

	// Продукты потрачены, и партнёрский вызов их не вернёт: такой случай заведение несёт
	// в саппорт площадки, а не отменяет сам.
	document := asRefusal(t, b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"), http.StatusConflict)
	if document.Code != "order_not_cancellable" {
		t.Errorf("код отказа %q, ожидали order_not_cancellable", document.Code)
	}

	if status := b.onlyIncomingOrder(t).Status; status != "cooking" {
		t.Errorf("состояние заказа %q, ожидали \"cooking\": отказанная отмена его не тронула", status)
	}
	if left := b.stockOf(t, item.ID); left != 3 {
		t.Errorf("остаток %d, ожидали 3: отказанная отмена вернула в меню продукты, которые уже в готовке",
			left)
	}
}

func TestRepeatedCancellationOfTheSameOrderReturnsItsProductsOnlyOnce(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 2)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	// Площадка повторяет отмену, не получив ответа: повтор принимается, но остаток
	// второй раз в меню не возвращается — иначе заведение продавало бы продукты,
	// которых у него нет.
	var first, second cancellation
	decode(t, b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"), http.StatusOK, &first)
	decode(t, b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"), http.StatusOK, &second)

	if !second.CancelledAt.Equal(first.CancelledAt) {
		t.Errorf("повтор отвечает временем отмены %s, а первая отмена случилась в %s",
			second.CancelledAt, first.CancelledAt)
	}
	if left := b.stockOf(t, item.ID); left != 5 {
		t.Errorf("остаток %d, ожидали 5: повторная отмена вернула продукты второй раз", left)
	}
}

func TestCancellationWithoutAReasonIsAcceptedAllTheSame(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 1)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	// Тела у просьбы отменить контракт не требует: объяснение полезно оператору, но
	// заказ отменяется и без него — иначе площадка не смогла бы отменить заказ, у
	// которого причины нет вовсе.
	var cancelled cancellation
	decode(t, b.do(t, http.MethodPost, contractOrders+"/"+order.OrderID+"/cancel", nil, nil),
		http.StatusOK, &cancelled)

	if status := b.onlyIncomingOrder(t).Status; status != "cancelled" {
		t.Errorf("состояние заказа %q, ожидали \"cancelled\"", status)
	}
}

func TestCancellationOfAnOrderTheBoxNeverAcceptedIsRefusedAsUnknown(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.cancel(t, uuid.NewString(), "Отмена по просьбе покупателя"),
		http.StatusNotFound)
	if document.Code != "order_not_found" {
		t.Errorf("код отказа %q, ожидали order_not_found: молчаливое согласие означало бы, "+
			"что площадка считает отменённым заказ, о котором здесь не знают", document.Code)
	}
}

func TestOrderCancelledByThePlatformIsNotMovedByTheOperatorAnyFurther(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 1)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}

	incoming := b.onlyIncomingOrder(t)
	if resp := b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"); resp.Status != http.StatusOK {
		t.Fatalf("заказ не отменён: %d %s", resp.Status, resp.Body)
	}

	// Отмена — обрыв пути, а не ступень: оператор не готовит то, что у него забрали.
	if resp := b.confirm(t, incoming.ID); resp.Status == http.StatusOK {
		t.Error("оператор взялся за отменённый заказ")
	}
	if resp := b.startCooking(t, incoming.ID); resp.Status == http.StatusOK {
		t.Error("отменённый заказ ушёл в готовку")
	}
	if status := b.onlyIncomingOrder(t).Status; status != "cancelled" {
		t.Errorf("состояние заказа %q, ожидали \"cancelled\"", status)
	}
}

func TestCancellationAskedForByThePlatformIsNotSentBackToItAsANotification(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	order := orderFor(item, 1)

	if resp := b.place(t, order); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}
	if resp := b.cancel(t, order.OrderID, "Отмена по просьбе покупателя"); resp.Status != http.StatusOK {
		t.Fatalf("заказ не отменён: %d %s", resp.Status, resp.Body)
	}

	// Очередь уведомлений везёт площадке новости с кухни, а отмену затеяла она сама:
	// эхо её собственного решения не сообщает ей ничего, зато стоит доставки.
	b.runDelivery(t, time.Now())

	if statuses := b.platform.statuses(); len(statuses) != 0 {
		t.Errorf("площадке уехали уведомления %v, а она сама и просила отменить заказ", statuses)
	}
}
