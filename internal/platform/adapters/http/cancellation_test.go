// Отмена заказа проверяется через тот же шов, что и всё остальное: настоящий роутер
// площадки со всеми middleware, настоящий PostgreSQL, заведение — та же HTTP-заглушка,
// что принимает заказы. Отмена уезжает к ней по протоколу, и тест смотрит на то, что
// доехало, а не на то, какой метод площадка собиралась вызвать.
//
// Покупатель отменяет заказ своей ручкой, саппорт — админской: обе ходят в одну и ту же
// матрицу переходов, и разница между ними — только в том, кому что разрешено и кто обязан
// объяснить, почему.
package http_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// supportCancelPath — админская ручка отмены заказа: та самая «через саппорта».
const supportCancelPath = "/api/admin/v1/orders/"

// cancellation — отмена, доехавшая до заведения по партнёрскому контракту.
type cancellation struct {
	// OrderID — заказ, который просят отменить: он взят из пути, а не из тела, — так его
	// называет контракт.
	OrderID string
	// Reason — причина для оператора заведения.
	Reason string
}

// cancelOrder принимает отмену так, как это делает заведение по партнёрскому контракту:
// запоминает просьбу и подтверждает отмену.
func (b *box) cancelOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	// Тела у просьбы может не быть вовсе — контракт его не требует, и заглушка ведёт
	// себя как настоящая коробка: пустое тело означает отмену без объяснения.
	_ = json.NewDecoder(r.Body).Decode(&body)

	orderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/orders/"), "/cancel")

	b.mu.Lock()
	b.cancellations = append(b.cancellations, cancellation{OrderID: orderID, Reason: body.Reason})
	refusal := b.cancelRefusal
	b.mu.Unlock()

	// Отказ отдаётся после того, как просьба записана: отмена до заведения доехала, и
	// тест вправе смотреть, какой она приехала, даже когда заведение её не приняло.
	if refusal != nil {
		refusal.write(w)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"order_id":     orderID,
		"cancelled_at": time.Now().UTC(),
	})
}

// cancelled отдаёт отмены, которые площадка донесла до заведения.
func (b *box) cancelled() []cancellation {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]cancellation(nil), b.cancellations...)
}

// refuseCancellations заставляет заведение отказывать в отмене: так отвечает коробка,
// которая уже начала готовить заказ.
func (b *box) refuseCancellations(refusal boxRefusal) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cancelRefusal = &refusal
}

// cancelOrder отменяет заказ от имени покупателя.
func (p *platform) cancelOrder(t *testing.T, customer, order uuid.UUID) response {
	t.Helper()

	return p.asCustomer(t, customer, http.MethodPost, ordersPath+"/"+order.String()+"/cancel", "")
}

// cancelBySupport отменяет заказ админской ручкой — так это делает саппорт, разобравший
// обращение заведения.
func (p *platform) cancelBySupport(t *testing.T, order uuid.UUID, reason, token string) response {
	t.Helper()

	body := fmt.Sprintf(`{"reason":%q}`, reason)

	return p.do(t, http.MethodPost, supportCancelPath+order.String()+"/cancel", token, []byte(body))
}

func TestCustomerCancelsTheOrderUntilTheRestaurantStartsCooking(t *testing.T) {
	t.Parallel()

	// Заказ отменяется из каждого состояния, в котором продукты ещё не потрачены: сразу
	// после оформления, пока заведение его рассматривает, и после того, как оно взялось.
	for _, status := range []string{"", "confirmed"} {
		platform := newPlatform(t)
		bakery := newBox(t)
		accepted := platform.acceptOrder(t, bakery)

		if status != "" {
			platform.advance(t, accepted.restaurant.Key, accepted.id, status)
		}

		order := asOrder(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusOK)

		if order.Status != "cancelled" {
			t.Errorf("статус заказа, отменённого из %q, — %q, ожидали \"cancelled\"",
				status, order.Status)
		}
		if order.CancelledBy != "customer" {
			t.Errorf("инициатор отмены %q, ожидали \"customer\"", order.CancelledBy)
		}
		if order.CancellationReason != "" {
			t.Errorf("причина отмены %q: покупатель объяснять свою отмену не обязан",
				order.CancellationReason)
		}
	}
}

func TestOrderNotYetSeenByTheRestaurantIsCancelledByTheCustomerToo(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	// Заведение молчит на переданный заказ: заказ записан и остался в `pending` — о нём
	// знает одна площадка. Отменить его покупатель вправе так же, как всякий другой.
	silent := newBox(t)
	silent.answerWith(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "заведение недоступно", http.StatusBadGateway)
	})

	registered := platform.connect(t, "Пекарня «Батон»", silent)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1}))

	if status := platform.storedOrderStatus(t, id); status != "pending" {
		t.Fatalf("статус заказа %q, ожидали \"pending\"", status)
	}

	order := asOrder(t, platform.cancelOrder(t, customer, id), http.StatusOK)
	if order.Status != "cancelled" {
		t.Errorf("статус заказа %q, ожидали \"cancelled\": заказ записан, и отменять его покупателю "+
			"не мешает молчащее заведение", order.Status)
	}
}

func TestCustomerCancellationReachesTheRestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	asOrder(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusOK)

	cancellations := bakery.cancelled()
	if len(cancellations) != 1 {
		t.Fatalf("до заведения доехало %d отмен, ожидали одну: заведение готовит заказ, "+
			"о котором покупатель уже передумал", len(cancellations))
	}
	if cancellations[0].OrderID != accepted.id.String() {
		t.Errorf("заведению отменили заказ %s, а покупатель отменял %s",
			cancellations[0].OrderID, accepted.id)
	}
	if cancellations[0].Reason == "" {
		t.Error("отмена приехала заведению без единого слова: оператор читает такую отмену " +
			"как сбой интеграции")
	}
}

func TestOrderBeingCookedIsNotCancelledByTheCustomer(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	for _, status := range []string{"confirmed", "cooking"} {
		platform.advance(t, accepted.restaurant.Key, accepted.id, status)
	}

	document := asRefusal(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusConflict)
	if document.Code != "order_not_cancellable" {
		t.Errorf("код отказа %q, ожидали order_not_cancellable: продукты уже потрачены", document.Code)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "cooking" {
		t.Errorf("статус заказа %q, ожидали \"cooking\": отказанная отмена его не тронула", status)
	}
	if cancellations := bakery.cancelled(); len(cancellations) != 0 {
		t.Errorf("до заведения доехало %d отмен, а площадка отказала покупателю", len(cancellations))
	}
}

func TestRepeatedCancellationByTheCustomerChangesNothing(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	asOrder(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusOK)

	// Повторное нажатие «Отменить» не ломается и второй отмены заведению не привозит:
	// заказ уже отменён, и менять в нём нечего.
	order := asOrder(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusOK)
	if order.Status != "cancelled" {
		t.Errorf("статус заказа %q, ожидали \"cancelled\"", order.Status)
	}

	if cancellations := bakery.cancelled(); len(cancellations) != 1 {
		t.Errorf("до заведения доехало %d отмен, ожидали одну", len(cancellations))
	}

	history := journalStatuses(platform.statusJournal(t, accepted.id))
	want := []string{"pending", "under_review", "cancelled"}
	if len(history) != len(want) {
		t.Errorf("история заказа %v, ожидали %v: повторная отмена её не удваивает", history, want)
	}
}

func TestCustomerDoesNotCancelSomebodyElsesOrder(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	// Чужой заказ неотличим от несуществующего: по разным ответам перебор
	// идентификаторов рассказал бы, какие заказы на площадке есть.
	stranger := asRefusal(t, platform.cancelOrder(t, uuid.New(), accepted.id), http.StatusNotFound)
	if stranger.Code != "order_not_found" {
		t.Errorf("код отказа %q, ожидали order_not_found", stranger.Code)
	}

	unknown := asRefusal(t, platform.cancelOrder(t, accepted.customer, newOrderID(t)), http.StatusNotFound)
	if unknown.Code != "order_not_found" {
		t.Errorf("код отказа на несуществующий заказ %q, ожидали order_not_found", unknown.Code)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": посторонний его не отменяет", status)
	}
}

func TestSupportCancelsTheOrderBeingCookedWithAReasonTheCustomerSees(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	for _, status := range []string{"confirmed", "cooking"} {
		platform.advance(t, accepted.restaurant.Key, accepted.id, status)
	}

	// Заведение позвонило в саппорт: продукты потрачены, отменить заказ само оно уже не
	// может — и отмену делает человек площадки, называя причину.
	resp := platform.cancelBySupport(t, accepted.id, "Заведение сообщило: сгорела печь", adminToken)
	if resp.Status != http.StatusOK {
		t.Fatalf("саппорт не отменил заказ: %d %s", resp.Status, resp.Body)
	}

	order := asOrder(t, platform.order(t, accepted.customer, accepted.id), http.StatusOK)

	if order.Status != "cancelled" {
		t.Errorf("покупатель видит статус %q, ожидали \"cancelled\"", order.Status)
	}
	if order.CancelledBy != "support" {
		t.Errorf("инициатор отмены %q, ожидали \"support\": решение принимал человек площадки, "+
			"а не кнопка на кухне", order.CancelledBy)
	}
	if order.CancellationReason != "Заведение сообщило: сгорела печь" {
		t.Errorf("причина отмены %q: покупателю показывают именно её", order.CancellationReason)
	}
}

func TestSupportCancellationIsWrittenToTheJournalAsMadeBySupport(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	platform.advance(t, accepted.restaurant.Key, accepted.id, "confirmed")

	resp := platform.cancelBySupport(t, accepted.id, "Заведение сообщило: закончились продукты", adminToken)
	if resp.Status != http.StatusOK {
		t.Fatalf("саппорт не отменил заказ: %d %s", resp.Status, resp.Body)
	}

	journal := platform.statusJournal(t, accepted.id)
	last := journal[len(journal)-1]

	if last.Status != "cancelled" || last.Actor != "support" {
		t.Errorf("последняя запись журнала — %q по требованию %q, ожидали \"cancelled\" от \"support\": "+
			"инициатор отмены должен быть виден тому, кто разбирает жалобу", last.Status, last.Actor)
	}
}

func TestSupportCancellationWithoutAReasonIsRefused(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	for _, body := range []string{`{}`, `{"reason":"   "}`} {
		resp := platform.do(t, http.MethodPost,
			supportCancelPath+accepted.id.String()+"/cancel", adminToken, []byte(body))

		if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
			t.Errorf("отказ на тело %s с кодом %q, ожидали validation_failed", body, document.Code)
		}
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": отмена без причины его не тронула", status)
	}
}

func TestSupportCancellationReachesTheRestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	resp := platform.cancelBySupport(t, accepted.id, "Заведение сообщило: закончились продукты", adminToken)
	if resp.Status != http.StatusOK {
		t.Fatalf("саппорт не отменил заказ: %d %s", resp.Status, resp.Body)
	}

	cancellations := bakery.cancelled()
	if len(cancellations) != 1 {
		t.Fatalf("до заведения доехало %d отмен, ожидали одну", len(cancellations))
	}
	if cancellations[0].Reason != "Заведение сообщило: закончились продукты" {
		t.Errorf("заведению приехала причина %q, ожидали ту, которую назвал саппорт",
			cancellations[0].Reason)
	}
}

func TestOrderCancelledBySupportStaysCancelledEvenIfTheRestaurantRefusesToCancelIt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	// Заведение уже начало готовить и отменять отказывается — площадка узнала об этом
	// после того, как приняла решение. Судьбу заказа решает она, а не коробка: покупателю
	// обещана отмена, и разбирает такой случай тот же человек, который её сделал.
	bakery.refuseCancellations(boxRefusal{Code: "order_not_cancellable", Detail: "заказ уже готовится"})

	resp := platform.cancelBySupport(t, accepted.id, "Заведение сообщило: закончились продукты", adminToken)
	if resp.Status != http.StatusOK {
		t.Fatalf("саппорт не отменил заказ: %d %s", resp.Status, resp.Body)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "cancelled" {
		t.Errorf("статус заказа %q, ожидали \"cancelled\"", status)
	}
}

func TestSupportDoesNotCancelAnOrderThatHasAlreadyLivedItsLife(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	for _, status := range []string{"confirmed", "cooking", "delivering", "completed"} {
		platform.advance(t, accepted.restaurant.Key, accepted.id, status)
	}

	resp := platform.cancelBySupport(t, accepted.id, "Заведение сообщило: закончились продукты", adminToken)
	if document := asRefusal(t, resp, http.StatusConflict); document.Code != "order_not_cancellable" {
		t.Errorf("код отказа %q, ожидали order_not_cancellable: доставленный заказ отменять нечего",
			document.Code)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "completed" {
		t.Errorf("статус заказа %q, ожидали \"completed\"", status)
	}
}

func TestOrderIsNotCancelledBySupportWithoutTheAdminToken(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	// Без токена — `401`; партнёрский ключ опознан, но админскую операцию не открывает —
	// `403`: заведение отменяет заказ через саппорта, а не своим ключом.
	unauthorized := platform.cancelBySupport(t, accepted.id, "Закончились продукты", "")
	if document := asRefusal(t, unauthorized, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}

	forbidden := platform.cancelBySupport(t, accepted.id, "Закончились продукты", accepted.restaurant.Key)
	if document := asRefusal(t, forbidden, http.StatusForbidden); document.Code != "forbidden" {
		t.Errorf("код отказа %q, ожидали forbidden", document.Code)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\"", status)
	}
}

func TestSupportDoesNotCancelAnOrderThePlatformDoesNotKnow(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.cancelBySupport(t, newOrderID(t), "Заведение сообщило: закончились продукты", adminToken)
	if document := asRefusal(t, resp, http.StatusNotFound); document.Code != "order_not_found" {
		t.Errorf("код отказа %q, ожидали order_not_found", document.Code)
	}
}

func TestRestaurantThatStartedCookingCannotCancelTheOrderThroughThePartnerAPI(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	for _, status := range []string{"confirmed", "cooking"} {
		platform.advance(t, accepted.restaurant.Key, accepted.id, status)
	}

	// Партнёрский контракт даёт заведению один способ отменить заказ — уведомление об
	// отмене, — и после начала готовки площадка его не принимает: продукты потрачены, и
	// такую отмену делает саппорт, а не заведение.
	resp := platform.notify(t, accepted.restaurant.Key, accepted.id,
		cancellationNotification("Закончились продукты"))
	if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "cooking" {
		t.Errorf("статус заказа %q, ожидали \"cooking\": заведение отменило заказ мимо саппорта", status)
	}
}

func TestCancelledOrderIsNotMovedAnyFurtherByTheRestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	asOrder(t, platform.cancelOrder(t, accepted.customer, accepted.id), http.StatusOK)

	// Отменённый заказ терминален: уведомление заведения о том, что его готовят, принято
	// (повторять его незачем) и ничего не меняет.
	if state := platform.advance(t, accepted.restaurant.Key, accepted.id, "cooking"); state != "cancelled" {
		t.Errorf("площадка объявила статус %q, ожидали \"cancelled\"", state)
	}

	if status := platform.storedOrderStatus(t, accepted.id); status != "cancelled" {
		t.Errorf("статус заказа %q, ожидали \"cancelled\": из отменённого заказа переходов нет", status)
	}
}
