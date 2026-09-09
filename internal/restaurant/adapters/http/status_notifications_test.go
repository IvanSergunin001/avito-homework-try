// Продвижение заказа оператором и доставка статуса площадке проверяются через тот же шов
// коробки: настоящий роутер со всеми middleware, настоящий PostgreSQL, а площадка по ту
// сторону — HTTP-приёмник уведомлений. Подменять исходящий вызов моком значило бы
// проверять, что коробка зовёт нужный метод, вместо того чтобы проверять, что она
// разговаривает по партнёрскому контракту.
//
// Доставка запускается тестом с названным моментом, а не ожиданием тика: тик — тонкая
// обёртка над тем же сценарием, и тест, который спит в надежде на таймер, проверял бы
// планировщик Go. А вот пауза перед повтором — это правило, и сдвиг названного момента
// вперёд проверяет именно её.
package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// partnerKey — партнёрский ключ, выданный заведению площадкой: им закрыты её
	// партнёрские операции, и уведомление без него площадка не примет.
	partnerKey = "pk_TESTKEYTESTKEYTESTKEYTESTKE"
	// waitForPlatform — сколько коробка ждёт ответа площадки на уведомление. Секунды
	// хватает с запасом: площадка стоит в том же процессе.
	waitForPlatform = time.Second
	// notificationsPath — партнёрская поверхность площадки: сюда коробка привозит статус.
	// Он записан строкой, а не взят из константы площадки: коробка знает о ней ровно то,
	// что та опубликовала контрактом, и тест проверяет именно этот путь.
	notificationsPath = "/api/kitchen/v1/partner/orders/"
)

// notified — уведомление, доехавшее до площадки, вместе с тем, чем оно было закрыто.
type notified struct {
	// OrderID — заказ, о котором уведомили: он взят из пути, а не из тела.
	OrderID string
	// Authorization — заголовок, которым коробка закрыла вызов.
	Authorization string

	Status     string    `json:"status"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

// platformStub — площадка по ту сторону партнёрского контракта.
//
// Это заглушка, а не подменённый клиент: шов коробки проходит по границе протокола, и
// проверять здесь нужно то, что уехало по HTTP, — путь, ключ и тело уведомления.
type platformStub struct {
	srv *httptest.Server

	mu sync.Mutex
	// received — уведомления, которые площадка приняла, в порядке приезда.
	received []notified
	// visits — сколько раз коробка приходила: неудачные попытки тоже считаются.
	visits int
	// down — площадка недоступна: отвечает отказом, ничего не принимая. Так изображается
	// её перезапуск или обрыв сети.
	down bool
}

// newPlatformStub поднимает доступную площадку.
func newPlatformStub(t *testing.T) *platformStub {
	t.Helper()

	p := &platformStub{}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)

	return p
}

// handle принимает уведомление о смене статуса заказа.
func (p *platformStub) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.visits++

	if p.down {
		http.Error(w, "площадка недоступна", http.StatusServiceUnavailable)

		return
	}

	orderID, ok := strings.CutPrefix(r.URL.Path, notificationsPath)
	if !ok || !strings.HasSuffix(orderID, "/status") || r.Method != http.MethodPost {
		http.Error(w, "такой операции у площадки нет", http.StatusNotFound)

		return
	}

	notification := notified{
		OrderID:       strings.TrimSuffix(orderID, "/status"),
		Authorization: r.Header.Get("Authorization"),
	}
	if err := json.NewDecoder(r.Body).Decode(&notification); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	p.received = append(p.received, notification)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"order_id":"` + notification.OrderID + `","status":"` + notification.Status + `"}`))
}

// breakDown делает площадку недоступной: она отвечает отказом, ничего не принимая.
func (p *platformStub) breakDown() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.down = true
}

// restore возвращает площадку в строй.
func (p *platformStub) restore() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.down = false
}

// notifications отдаёт уведомления, которые площадка приняла.
func (p *platformStub) notifications() []notified {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]notified(nil), p.received...)
}

// statuses перечисляет статусы принятых уведомлений в порядке приезда.
func (p *platformStub) statuses() []string {
	received := p.notifications()

	statuses := make([]string, 0, len(received))
	for _, notification := range received {
		statuses = append(statuses, notification.Status)
	}

	return statuses
}

// visited — сколько раз коробка приходила к площадке, включая неудачные попытки.
func (p *platformStub) visited() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.visits
}

// runDelivery развозит уведомления, которым к моменту at пора ехать.
func (b *box) runDelivery(t *testing.T, at time.Time) {
	t.Helper()

	if err := b.background.Notifications.Deliver(t.Context(), at); err != nil {
		t.Fatalf("развезти уведомления: %v", err)
	}
}

// queued — строка очереди уведомлений: то, что видно в данных коробки.
type queued struct {
	Attempts  int
	Delivered bool
	Failed    bool
	LastError string
}

// queuedNotification читает строку очереди прямо из базы коробки: очередь — это её
// собственные данные, наружу они не показываются ни одной поверхностью.
func (b *box) queuedNotification(t *testing.T, orderID, status string) queued {
	t.Helper()

	var row queued
	err := b.db.QueryRowContext(t.Context(), `
		SELECT attempts, delivered_at IS NOT NULL, failed_at IS NOT NULL, last_error
		FROM restaurant.status_notifications
		WHERE order_id = $1 AND status = $2`, orderID, status).
		Scan(&row.Attempts, &row.Delivered, &row.Failed, &row.LastError)
	if err != nil {
		t.Fatalf("прочитать уведомление о заказе %s (%s): %v", orderID, status, err)
	}

	return row
}

// startCooking — оператор отправляет заказ в готовку.
func (b *box) startCooking(t *testing.T, id string) response {
	t.Helper()

	return b.send(t, http.MethodPost, operatorOrders+"/"+id+"/start-cooking", nil)
}

// startDelivery — оператор отправляет заказ в доставку.
func (b *box) startDelivery(t *testing.T, id string) response {
	t.Helper()

	return b.send(t, http.MethodPost, operatorOrders+"/"+id+"/start-delivery", nil)
}

// complete — оператор завершает заказ.
func (b *box) complete(t *testing.T, id string) response {
	t.Helper()

	return b.send(t, http.MethodPost, operatorOrders+"/"+id+"/complete", nil)
}

// confirmedOrder заводит позицию, передаёт коробке заказ на неё и подтверждает его:
// с этого места заказ идёт по кухне.
func confirmedOrder(t *testing.T, b *box) operatorOrder {
	t.Helper()

	incoming := acceptedOrder(t, b, 1, 5)
	if resp := b.confirm(t, incoming.ID); resp.Status != http.StatusOK {
		t.Fatalf("заказ не подтверждён: %d %s", resp.Status, resp.Body)
	}

	return b.onlyIncomingOrder(t)
}

func TestOperatorLeadsTheOrderFromCookingThroughDeliveryToCompletion(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)

	for _, step := range []struct {
		move   func(t *testing.T, id string) response
		status string
	}{
		{move: b.startCooking, status: "cooking"},
		{move: b.startDelivery, status: "delivering"},
		{move: b.complete, status: "completed"},
	} {
		var moved operatorOrder
		decode(t, step.move(t, order.ID), http.StatusOK, &moved)

		if moved.Status != step.status {
			t.Fatalf("состояние заказа %q, ожидали %q", moved.Status, step.status)
		}
		if listed := b.onlyIncomingOrder(t).Status; listed != step.status {
			t.Fatalf("в списке заказ остался в %q: оператор увидел бы своё решение только после перезапуска",
				listed)
		}
	}
}

func TestOrderDoesNotSkipCookingOnItsWayToDelivery(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)

	refusal := asRefusal(t, b.startDelivery(t, order.ID), http.StatusConflict)
	if refusal.Code != "transition_not_allowed" {
		t.Errorf("причина отказа %q, ожидали transition_not_allowed", refusal.Code)
	}
	if listed := b.onlyIncomingOrder(t).Status; listed != "confirmed" {
		t.Errorf("состояние заказа %q, ожидали confirmed: заказ уехал, не будучи приготовленным", listed)
	}
}

func TestOrderIsNotCookedBeforeTheOperatorTookIt(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 1, 5)

	refusal := asRefusal(t, b.startCooking(t, incoming.ID), http.StatusConflict)
	if refusal.Code != "transition_not_allowed" {
		t.Errorf("причина отказа %q, ожидали transition_not_allowed", refusal.Code)
	}
}

func TestCompletedOrderIsNotMovedAnyFurther(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)
	moveTo(t, b, order.ID, "completed")

	refusal := asRefusal(t, b.startCooking(t, order.ID), http.StatusConflict)
	if refusal.Code != "transition_not_allowed" {
		t.Errorf("причина отказа %q, ожидали transition_not_allowed", refusal.Code)
	}
	if listed := b.onlyIncomingOrder(t).Status; listed != "completed" {
		t.Errorf("состояние заказа %q, ожидали completed: доставленный заказ вернулся на кухню", listed)
	}
}

func TestEveryStatusChangeReachesThePlatformInTheOrderItHappened(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)
	moveTo(t, b, order.ID, "delivering")

	b.runDelivery(t, time.Now())

	statuses := b.platform.statuses()
	if len(statuses) != 3 {
		t.Fatalf("площадка получила %d уведомлений (%v), ожидали три: каждая смена статуса уведомляет её",
			len(statuses), statuses)
	}
	for at, expected := range []string{"confirmed", "cooking", "delivering"} {
		if statuses[at] != expected {
			t.Errorf("%d-е уведомление о %q, ожидали %q: покупатель увидел бы статусы не в том порядке",
				at+1, statuses[at], expected)
		}
	}
}

func TestNotificationCarriesThePlatformOrderAndThePartnerKeyOfTheRestaurant(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	item := b.addItem(t, newItem{Name: "Круассан с миндалём", Price: 45000, Stock: 5})
	placed := orderFor(item, 1)

	if resp := b.place(t, placed); resp.Status != http.StatusCreated {
		t.Fatalf("заказ не принят: %d %s", resp.Status, resp.Body)
	}
	if resp := b.confirm(t, b.onlyIncomingOrder(t).ID); resp.Status != http.StatusOK {
		t.Fatalf("заказ не подтверждён: %d %s", resp.Status, resp.Body)
	}

	b.runDelivery(t, time.Now())

	received := b.platform.notifications()
	if len(received) != 1 {
		t.Fatalf("площадка получила %d уведомлений, ожидали одно", len(received))
	}
	if received[0].OrderID != placed.OrderID {
		t.Errorf("уведомление о заказе %s, а площадка передавала %s: своего номера коробки она не знает",
			received[0].OrderID, placed.OrderID)
	}
	if received[0].Authorization != "Bearer "+partnerKey {
		t.Errorf("уведомление пришло с %q, ожидали партнёрский ключ заведения: без него площадка его не примет",
			received[0].Authorization)
	}
	if received[0].OccurredAt.IsZero() {
		t.Error("в уведомлении нет времени перехода: площадке нечем отличить запоздавшее от свежего")
	}
}

func TestRejectedOrderTellsThePlatformWhyItWasCancelled(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	incoming := acceptedOrder(t, b, 1, 5)

	if resp := b.reject(t, incoming.ID, "Закончились продукты"); resp.Status != http.StatusOK {
		t.Fatalf("заказ не отклонён: %d %s", resp.Status, resp.Body)
	}

	b.runDelivery(t, time.Now())

	received := b.platform.notifications()
	if len(received) != 1 {
		t.Fatalf("площадка получила %d уведомлений, ожидали одно", len(received))
	}
	if received[0].Status != "cancelled" || received[0].Reason != "Закончились продукты" {
		t.Errorf("уведомление %q с причиной %q, ожидали отмену с причиной оператора: её увидит покупатель",
			received[0].Status, received[0].Reason)
	}
}

func TestStatusReachesThePlatformAfterItWasBrieflyUnavailable(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)

	// Площадка перезапускается ровно в тот момент, когда коробка повезла ей статус.
	b.platform.breakDown()

	failed := time.Now()
	b.runDelivery(t, failed)

	if received := b.platform.notifications(); len(received) != 0 {
		t.Fatalf("площадка приняла %d уведомлений, а она недоступна", len(received))
	}
	if queued := b.queuedNotification(t, order.ID, "confirmed"); queued.Attempts != 1 || queued.Delivered {
		t.Fatalf("после неудачной попытки в очереди %+v, ожидали одну попытку и недоставленное уведомление",
			queued)
	}

	b.platform.restore()

	// Пауза перед повтором ещё не прошла: коробка не добивает площадку, которая только
	// что не ответила.
	b.runDelivery(t, failed.Add(retryBaseDelay/2))
	if received := b.platform.notifications(); len(received) != 0 {
		t.Errorf("коробка повторила доставку раньше назначенной паузы: %v", received)
	}

	b.runDelivery(t, failed.Add(retryBaseDelay))

	received := b.platform.notifications()
	if len(received) != 1 {
		t.Fatalf("площадка получила %d уведомлений, ожидали одно: статус теряться не должен", len(received))
	}
	if received[0].Status != "confirmed" {
		t.Errorf("площадке доехал статус %q, ожидали confirmed", received[0].Status)
	}
	if queued := b.queuedNotification(t, order.ID, "confirmed"); !queued.Delivered {
		t.Error("уведомление доехало, но в очереди осталось недоставленным: коробка повезёт его снова")
	}
}

func TestNotificationThatReachedThePlatformIsNotSentAgain(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	confirmedOrder(t, b)

	b.runDelivery(t, time.Now())
	b.runDelivery(t, time.Now().Add(time.Hour))

	if received := b.platform.notifications(); len(received) != 1 {
		t.Errorf("площадка получила %d уведомлений, ожидали одно: доставленное не везут повторно",
			len(received))
	}
}

func TestExhaustedDeliveryAttemptsStayVisibleInTheQueueAndInTheLog(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	order := confirmedOrder(t, b)
	b.platform.breakDown()

	// Каждая попытка — в свой момент: следующая назначена не раньше, чем через паузу,
	// которая удваивается с каждой неудачей.
	at := time.Now()
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		b.runDelivery(t, at)
		at = at.Add(retryBaseDelay << attempt)
	}

	queued := b.queuedNotification(t, order.ID, "confirmed")
	if queued.Attempts != retryAttempts {
		t.Errorf("в очереди %d попыток, ожидали %d", queued.Attempts, retryAttempts)
	}
	if !queued.Failed {
		t.Error("попытки кончились, но в данных это не видно: застрявший статус искать нечем")
	}
	if queued.LastError == "" {
		t.Error("в очереди не осталось ответа площадки: разбираться с недоставленным статусом не с чем")
	}

	if !strings.Contains(b.log.String(), "status notification delivery attempts exhausted") {
		t.Error("исчерпание попыток не попало в лог: о потерянном статусе никто не узнает вовремя")
	}

	// Исчерпавшее попытки уведомление больше не едет: очередь не наполняется вечными
	// повторами, которых никто не ждёт.
	visited := b.platform.visited()
	b.runDelivery(t, at.Add(time.Hour))
	if b.platform.visited() != visited {
		t.Error("коробка повезла уведомление после того, как попытки кончились")
	}
}

func TestOrdersWaitForTheOperatorUntilAutoAdvanceIsTurnedOn(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	confirmedOrder(t, b)

	b.advanceByTimer(t)

	if listed := b.onlyIncomingOrder(t).Status; listed != "confirmed" {
		t.Errorf("состояние заказа %q, ожидали confirmed: без флага статусы двигает оператор", listed)
	}
}

func TestAutoAdvanceMovesTheOrderOneStepAtATime(t *testing.T) {
	t.Parallel()

	b := boxOnAutopilot(t)
	acceptedOrder(t, b, 1, 5)

	for _, expected := range []string{"confirmed", "cooking", "delivering", "completed"} {
		b.advanceByTimer(t)

		if listed := b.onlyIncomingOrder(t).Status; listed != expected {
			t.Fatalf("состояние заказа %q, ожидали %q: покупатель должен увидеть каждую ступень",
				listed, expected)
		}
	}

	// Дальше двигать некуда: путь заказа пройден до конца.
	b.advanceByTimer(t)
	if listed := b.onlyIncomingOrder(t).Status; listed != "completed" {
		t.Errorf("состояние заказа %q, ожидали completed", listed)
	}
}

func TestAutoAdvancedStatusesReachThePlatformToo(t *testing.T) {
	t.Parallel()

	b := boxOnAutopilot(t)
	acceptedOrder(t, b, 1, 5)

	b.advanceByTimer(t)
	b.advanceByTimer(t)
	b.runDelivery(t, time.Now())

	statuses := b.platform.statuses()
	if len(statuses) != 2 || statuses[0] != "confirmed" || statuses[1] != "cooking" {
		t.Errorf("площадка получила %v, ожидали confirmed и cooking: статусы автопилота уезжают тем же путём",
			statuses)
	}
}

// advanceByTimer двигает заказы так, как это делает таймер коробки.
func (b *box) advanceByTimer(t *testing.T) {
	t.Helper()

	if err := b.background.Autopilot.Advance(t.Context()); err != nil {
		t.Fatalf("продвинуть заказы по таймеру: %v", err)
	}
}

// moveTo ведёт заказ по пути до названного состояния — так, как вёл бы его оператор.
func moveTo(t *testing.T, b *box, id, status string) {
	t.Helper()

	steps := []struct {
		move   func(t *testing.T, id string) response
		status string
	}{
		{move: b.startCooking, status: "cooking"},
		{move: b.startDelivery, status: "delivering"},
		{move: b.complete, status: "completed"},
	}

	for _, step := range steps {
		if resp := step.move(t, id); resp.Status != http.StatusOK {
			t.Fatalf("заказ не переведён в %s: %d %s", step.status, resp.Status, resp.Body)
		}
		if step.status == status {
			return
		}
	}

	t.Fatalf("состояние %q не лежит на пути заказа", status)
}
