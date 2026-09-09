// Уведомления о смене статуса проверяются через тот же шов, что и всё остальное:
// настоящий роутер площадки со всеми middleware, настоящий PostgreSQL, заведение — та же
// HTTP-заглушка, что принимает заказы. Уведомление приезжает по протоколу и с настоящим
// партнёрским ключом: подменять здесь нечего, а проверка «какой метод был вызван» не
// ответила бы на вопрос, увидит ли покупатель новый статус.
package http_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// statusNotification собирает тело уведомления о смене статуса по партнёрскому контракту.
//
// `occurred_at` — часы заведения: заглушка отмечает уведомление своим временем, как
// отмечала бы его настоящая коробка.
func statusNotification(status string) string {
	return fmt.Sprintf(`{"status":%q,"occurred_at":%q}`,
		status, time.Now().UTC().Format(time.RFC3339Nano))
}

// cancellationNotification собирает уведомление об отмене заказа заведением: у неё есть
// причина, и её увидит покупатель.
func cancellationNotification(reason string) string {
	return fmt.Sprintf(`{"status":"cancelled","occurred_at":%q,"reason":%q}`,
		time.Now().UTC().Format(time.RFC3339Nano), reason)
}

// notify отправляет площадке уведомление о смене статуса заказа партнёрским ключом key и
// возвращает ответ как есть.
func (p *platform) notify(t *testing.T, key string, order uuid.UUID, body string) response {
	t.Helper()

	return p.do(t, http.MethodPost,
		platformhttp.PartnerAPIPrefix+"/orders/"+order.String()+"/status", key, []byte(body))
}

// advance уведомляет площадку о новом статусе заказа и требует, чтобы уведомление было
// принято. Возвращает статус, который площадка объявила действующим.
func (p *platform) advance(t *testing.T, key string, order uuid.UUID, status string) string {
	t.Helper()

	return asStatusState(t, p.notify(t, key, order, statusNotification(status)), order)
}

// asStatusState разбирает ответ на уведомление: контракт обещает `200` и действующий
// статус заказа — по нему заведение видит, применилось ли уведомление.
func asStatusState(t *testing.T, resp response, order uuid.UUID) string {
	t.Helper()

	if resp.Status != http.StatusOK {
		t.Fatalf("площадка ответила на уведомление %d, ожидали 200: %s", resp.Status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var state struct {
		OrderID uuid.UUID `json:"order_id"`
		Status  string    `json:"status"`
	}
	if err := json.Unmarshal(resp.Body, &state); err != nil {
		t.Fatalf("разобрать ответ на уведомление: %v", err)
	}
	if state.OrderID != order {
		t.Errorf("ответ о заказе %s, ожидали %s", state.OrderID, order)
	}

	return state.Status
}

// acceptedOrder — заказ, переданный заведению: всё, чем его двигают дальше и чем смотрят
// на него глазами покупателя.
type acceptedOrder struct {
	// restaurant — заведение, которое его выполняет, вместе со своим партнёрским ключом.
	restaurant registration
	// customer — покупатель, оформивший заказ.
	customer uuid.UUID
	// id — идентификатор заказа.
	id uuid.UUID
}

// acceptOrder оформляет заказ, который заведение приняло: с этого начинается всякий тест
// уведомлений — двигать можно только тот заказ, который заведению передали.
func (p *platform) acceptOrder(t *testing.T, restaurant *box) acceptedOrder {
	t.Helper()

	registered := p.connect(t, "Пекарня «Батон»", restaurant)
	croissant := p.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	order := asOrder(t, p.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2})), http.StatusCreated)

	if order.Status != "under_review" {
		t.Fatalf("статус принятого заказа %q, ожидали \"under_review\"", order.Status)
	}

	return acceptedOrder{restaurant: registered, customer: customer, id: id}
}

// journalEntry — запись журнала состояний заказа, как её видно в хранилище площадки.
//
// Снаружи журнала не видно ни на одной ручке — он ведётся для того, кто разбирает жалобу,
// а не для покупателя, — поэтому тест читает его прямо из базы: другого способа спросить
// «что с заказом происходило» у площадки нет.
type journalEntry struct {
	Status     string
	Actor      string
	OccurredAt time.Time
}

// statusJournal читает журнал состояний заказа в том порядке, в каком он писался.
func (p *platform) statusJournal(t *testing.T, order uuid.UUID) []journalEntry {
	t.Helper()

	rows, err := p.db.QueryContext(t.Context(), `
		SELECT status, actor, occurred_at
		FROM platform.order_status_events
		WHERE order_id = $1
		ORDER BY id`, order)
	if err != nil {
		t.Fatalf("прочитать журнал заказа %s: %v", order, err)
	}
	defer func() { _ = rows.Close() }()

	var journal []journalEntry
	for rows.Next() {
		var entry journalEntry
		if err := rows.Scan(&entry.Status, &entry.Actor, &entry.OccurredAt); err != nil {
			t.Fatalf("разобрать запись журнала заказа %s: %v", order, err)
		}

		journal = append(journal, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("прочитать журнал заказа %s: %v", order, err)
	}

	return journal
}

// journalStatuses — состояния из журнала подряд: по ним читается сама история заказа.
func journalStatuses(journal []journalEntry) []string {
	statuses := make([]string, 0, len(journal))
	for _, entry := range journal {
		statuses = append(statuses, entry.Status)
	}

	return statuses
}

func TestCustomerSeesEveryStatusTheRestaurantNotifiesAbout(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	// Весь путь заказа приезжает уведомлениями, и покупатель видит его целиком: заказ
	// подтвердили, готовят, везут, привезли.
	for _, status := range []string{"confirmed", "cooking", "delivering", "completed"} {
		if state := platform.advance(t, accepted.restaurant.Key, accepted.id, status); state != status {
			t.Fatalf("площадка объявила статус %q, ожидали %q", state, status)
		}

		order := asOrder(t, platform.order(t, accepted.customer, accepted.id), http.StatusOK)
		if order.Status != status {
			t.Errorf("покупатель видит статус %q, ожидали %q", order.Status, status)
		}
	}
}

func TestPlatformNeverAsksTheRestaurantForTheStatusOfAnOrder(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	// Заведение видело площадку ровно один раз — когда та передала ему заказ. Дальше
	// инициатива у заведения: статусы площадка не опрашивает.
	delivered := bakery.visited()

	for _, status := range []string{"confirmed", "cooking"} {
		platform.advance(t, registered.Key, id, status)
	}

	if visits := bakery.visited(); visits != delivered {
		t.Errorf("площадка сходила к заведению ещё %d раз после передачи заказа: статусы она не опрашивает",
			visits-delivered)
	}
}

func TestRepeatedDeliveryOfTheSameNotificationLeavesTheOrderAndItsHistoryAsTheyWere(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	// Заведение повторяет уведомление, пока не получит ответ, — площадка обязана
	// принять повтор и ничего им не изменить.
	for range 3 {
		if state := platform.advance(t, registered.Key, id, "confirmed"); state != "confirmed" {
			t.Fatalf("площадка объявила статус %q, ожидали \"confirmed\"", state)
		}
	}

	if status := platform.storedOrderStatus(t, id); status != "confirmed" {
		t.Errorf("статус заказа %q, ожидали \"confirmed\"", status)
	}

	history := journalStatuses(platform.statusJournal(t, id))
	want := []string{"pending", "under_review", "confirmed"}
	if len(history) != len(want) {
		t.Fatalf("история заказа %v, ожидали %v: повтор уведомления её не удваивает", history, want)
	}
	for index, status := range want {
		if history[index] != status {
			t.Fatalf("история заказа %v, ожидали %v", history, want)
		}
	}
}

func TestNotificationAboutAnEarlierStatusAfterALaterOneIsDropped(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	platform.advance(t, registered.Key, id, "confirmed")
	platform.advance(t, registered.Key, id, "cooking")

	// Уведомление о подтверждении обогнало уведомление о готовке — заказ не возвращается
	// назад, и заведение узнаёт об этом из действующего статуса в ответе.
	if state := platform.advance(t, registered.Key, id, "confirmed"); state != "cooking" {
		t.Errorf("площадка объявила статус %q, ожидали \"cooking\": заказ не откатывается", state)
	}

	if status := platform.storedOrderStatus(t, id); status != "cooking" {
		t.Errorf("статус заказа %q, ожидали \"cooking\"", status)
	}
}

func TestNotificationAboutATerminalOrderIsAcceptedAndIgnored(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	for _, status := range []string{"confirmed", "cooking", "delivering", "completed"} {
		platform.advance(t, registered.Key, id, status)
	}

	// Заказ прожил свою жизнь. Опоздавшее уведомление принято — повторять его заведению
	// незачем, — и ничего не изменило.
	if state := platform.advance(t, registered.Key, id, "cooking"); state != "completed" {
		t.Errorf("площадка объявила статус %q, ожидали \"completed\"", state)
	}

	if status := platform.storedOrderStatus(t, id); status != "completed" {
		t.Errorf("статус заказа %q, ожидали \"completed\"", status)
	}
}

func TestRestaurantThatGaveUpTheOrderCancelsItWithAReasonTheCustomerSees(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)

	resp := platform.notify(t, accepted.restaurant.Key, accepted.id,
		cancellationNotification("Закончились продукты"))
	if state := asStatusState(t, resp, accepted.id); state != "cancelled" {
		t.Fatalf("площадка объявила статус %q, ожидали \"cancelled\"", state)
	}

	order := asOrder(t, platform.order(t, accepted.customer, accepted.id), http.StatusOK)

	if order.Status != "cancelled" {
		t.Errorf("покупатель видит статус %q, ожидали \"cancelled\"", order.Status)
	}
	if order.CancelledBy != "restaurant" {
		t.Errorf("инициатор отмены %q, ожидали \"restaurant\"", order.CancelledBy)
	}
	if order.CancellationReason != "Закончились продукты" {
		t.Errorf("причина отмены %q: покупателю показывают именно её", order.CancellationReason)
	}
}

func TestNotificationAboutATransitionTheOrderCannotMakeIsRefused(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	// Заказ, о доставке которого сообщают раньше, чем о начале готовки, — это не
	// опоздавшее уведомление, а потерянное: принять его значило бы согласиться с
	// историей, которой не было. Правило это принадлежит заказу, а не обработчику.
	resp := platform.notify(t, registered.Key, id, statusNotification("delivering"))

	if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if status := platform.storedOrderStatus(t, id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": отказанное уведомление его не тронуло", status)
	}
}

func TestNotificationAboutAStatusThePlatformSetsItselfIsRefused(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	// `pending` и `under_review` ставит сама площадка: уведомление о них — не опоздание,
	// а расхождение заведения с контрактом.
	for _, status := range []string{"pending", "under_review", "переваривается"} {
		resp := platform.notify(t, registered.Key, id, statusNotification(status))

		if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
			t.Errorf("отказ на статус %q с кодом %q, ожидали validation_failed", status, document.Code)
		}
	}
}

func TestNotificationWithoutTheStatusOrTheTimeItHappenedIsRefused(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	for _, body := range []string{`{"occurred_at":"2026-09-08T12:00:00Z"}`, `{"status":"cooking"}`} {
		resp := platform.notify(t, registered.Key, id, body)

		if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
			t.Errorf("отказ на тело %s с кодом %q, ожидали validation_failed", body, document.Code)
		}
	}
}

func TestOrderIsMovedOnlyByTheRestaurantThatIsPreparingIt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	// Соседнее заведение подключено к площадке и со своим ключом ходит по той же ручке.
	// Чужой заказ для него неотличим от несуществующего: по разным ответам перебор
	// идентификаторов рассказал бы, какие заказы есть у соседей.
	neighbour := platform.connect(t, "Кофейня «Заря»", newBox(t))

	resp := platform.notify(t, neighbour.Key, id, statusNotification("confirmed"))
	if document := asRefusal(t, resp, http.StatusNotFound); document.Code != "order_not_found" {
		t.Errorf("код отказа %q, ожидали order_not_found", document.Code)
	}

	if status := platform.storedOrderStatus(t, id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": сосед его не двигает", status)
	}

	// Заказа, которого нет вовсе, для заведения тоже нет — и ответ тот же самый.
	unknown := platform.notify(t, registered.Key, newOrderID(t), statusNotification("confirmed"))
	if document := asRefusal(t, unknown, http.StatusNotFound); document.Code != "order_not_found" {
		t.Errorf("код отказа на несуществующий заказ %q, ожидали order_not_found", document.Code)
	}
}

func TestStatusOfTheOrderIsNotMovedWithoutThePartnerKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	id := platform.acceptOrder(t, bakery).id

	// Ключа нет вовсе — `401` с названием схемы: без него звонящий знает, что его не
	// пустили, но не знает, чем стучаться.
	unauthorized := platform.notify(t, "", id, statusNotification("confirmed"))
	if document := asRefusal(t, unauthorized, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	if !strings.Contains(unauthorized.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", unauthorized.Challenge)
	}

	// Ключ предъявлен, но площадка его не выдавала: чем именно он плох, наружу не
	// уходит — ответ тот же самый.
	unknown := platform.notify(t, "pk_подобранный-ключ", id, statusNotification("confirmed"))
	if document := asRefusal(t, unknown, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}

	if status := platform.storedOrderStatus(t, id); status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": без ключа заказ не двигают", status)
	}
}

func TestEveryStatusChangeOfTheOrderIsWrittenToTheJournalWithItsActorAndTime(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	accepted := platform.acceptOrder(t, bakery)
	registered, id := accepted.restaurant, accepted.id

	platform.advance(t, registered.Key, id, "confirmed")
	platform.advance(t, registered.Key, id, "cooking")

	journal := platform.statusJournal(t, id)
	want := []journalEntry{
		{Status: "pending", Actor: "customer"},
		{Status: "under_review", Actor: "restaurant"},
		{Status: "confirmed", Actor: "restaurant"},
		{Status: "cooking", Actor: "restaurant"},
	}

	if len(journal) != len(want) {
		t.Fatalf("в журнале %v, ожидали %v: каждое изменение статуса записано",
			journalStatuses(journal), journalStatuses(want))
	}

	for index, entry := range want {
		switch {
		case journal[index].Status != entry.Status:
			t.Errorf("запись %d о %q, ожидали %q", index, journal[index].Status, entry.Status)
		case journal[index].Actor != entry.Actor:
			t.Errorf("инициатор записи о %q — %q, ожидали %q",
				entry.Status, journal[index].Actor, entry.Actor)
		case journal[index].OccurredAt.IsZero():
			t.Errorf("у записи о %q нет времени: журнал отвечает на вопрос, когда это случилось",
				entry.Status)
		}
	}

	// История идёт по возрастанию времени: журнал ведётся по часам площадки, а не по
	// часам заведения, которые с ними не сверены.
	for index := 1; index < len(journal); index++ {
		if journal[index].OccurredAt.Before(journal[index-1].OccurredAt) {
			t.Errorf("запись о %q старше предыдущей о %q",
				journal[index].Status, journal[index-1].Status)
		}
	}
}
