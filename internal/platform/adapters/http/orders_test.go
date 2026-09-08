// Оформление заказа проверяется через тот же шов, что и всё остальное: настоящий роутер
// площадки, настоящий PostgreSQL, заведение — HTTP-заглушка, отвечающая по партнёрскому
// контракту. Исходящий клиент площадки не подменяется интерфейсом: тест смотрит на то,
// что уехало по протоколу, а не на то, какой метод был вызван, — иначе он подтверждал бы
// намерение площадки вместо её разговора с заведением.
package http_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ordersPath — клиентская ручка оформления заказа.
const ordersPath = "/api/v1/orders"

// silence — сколько молчит заведение, которое тест изображает не отвечающим вовремя.
// Заметно дольше, чем его готова ждать поднятая для такого теста площадка.
const silence = 2 * time.Second

// delivery — заказ, доставленный заведению: тело по контракту и заголовки, которыми
// площадка обязана его сопроводить.
type delivery struct {
	// Key — заголовок `Idempotency-Key`: по нему заведение узнаёт повторную отправку.
	Key string
	// Attempt — заголовок `Idempotency-Attempt`: номер попытки доставки.
	Attempt string
	// Order — тело заказа в схеме партнёрского контракта.
	Order incomingOrderDocument
	// StatusOnPlatform — состояние этого заказа в хранилище площадки в тот момент, когда
	// заведение о заказе узнало.
	StatusOnPlatform string
}

// incomingOrderDocument и incomingItemDocument — заказ в схеме партнёрского контракта:
// заглушка разбирает его именно ею, а не внутренними типами площадки, иначе тест
// проверял бы разговор площадки с самой собой.
type incomingOrderDocument struct {
	OrderID  uuid.UUID              `json:"order_id"`
	Address  string                 `json:"address"`
	Items    []incomingItemDocument `json:"items"`
	Total    int64                  `json:"total"`
	PlacedAt *time.Time             `json:"placed_at"`
}

type incomingItemDocument struct {
	ExternalID string `json:"external_id"`
	Quantity   int    `json:"quantity"`
	Price      int64  `json:"price"`
}

// acceptOrder принимает заказ так, как это делает заведение по партнёрскому контракту:
// запоминает доставку и подтверждает приём.
func (b *box) acceptOrder(w http.ResponseWriter, r *http.Request) {
	var order incomingOrderDocument
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	arrived := delivery{
		Key:     r.Header.Get("Idempotency-Key"),
		Attempt: r.Header.Get("Idempotency-Attempt"),
		Order:   order,
	}

	b.mu.Lock()
	watch := b.watch
	b.mu.Unlock()

	// Заглядывание в площадку идёт до ответа: заведение узнало о заказе, и вопрос теста
	// именно в том, что площадка успела записать к этому моменту.
	if watch != nil {
		arrived.StatusOnPlatform = watch(order.OrderID)
	}

	b.mu.Lock()
	b.deliveries = append(b.deliveries, arrived)
	refusal := b.refusal
	b.mu.Unlock()

	// Отказ отдаётся после того, как доставка записана: заказ до заведения доехал, и тест
	// вправе смотреть, каким он приехал, даже когда заведение его не взяло.
	if refusal != nil {
		refusal.write(w)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"order_id":    order.OrderID,
		"accepted_at": time.Now().UTC(),
	})
}

// delivered отдаёт заказы, которые площадка передала заведению.
func (b *box) delivered() []delivery {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]delivery(nil), b.deliveries...)
}

// observePlatform учит заглушку заглядывать в площадку, пока та ждёт её ответа.
func (b *box) observePlatform(watch func(order uuid.UUID) string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.watch = watch
}

// customerOrder — заказ глазами покупателя: разбираем его так же, как разбирал бы
// покупательский фронтенд, — по схеме из спецификации, а не по внутренним типам площадки.
type customerOrder struct {
	ID           uuid.UUID           `json:"id"`
	CustomerID   uuid.UUID           `json:"customer_id"`
	RestaurantID uuid.UUID           `json:"restaurant_id"`
	Status       string              `json:"status"`
	Address      string              `json:"address"`
	Items        []customerOrderItem `json:"items"`
	Total        int64               `json:"total"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
	// CancelledBy и CancellationReason появляются только у отменённого заказа: у
	// неотменённого этих полей в ответе нет вовсе.
	CancelledBy        string `json:"cancelled_by"`
	CancellationReason string `json:"cancellation_reason"`
}

// customerOrderItem — позиция заказа глазами покупателя.
type customerOrderItem struct {
	ProductID uuid.UUID `json:"product_id"`
	Name      string    `json:"name"`
	Price     int64     `json:"price"`
	Quantity  int       `json:"quantity"`
}

// ordered — строка корзины, которую тест кладёт в заказ.
type ordered struct {
	// product — товар площадки.
	product uuid.UUID
	// quantity — сколько его взяли.
	quantity int
}

// orderBody собирает тело оформления заказа. Суммы в нём нет: клиент её не присылает —
// её считает площадка.
func orderBody(order, restaurant uuid.UUID, address string, items ...ordered) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf(`{"product_id":%q,"quantity":%d}`, item.product, item.quantity))
	}

	return fmt.Sprintf(`{"order_id":%q,"restaurant_id":%q,"address":%q,"items":[%s]}`,
		order, restaurant, address, strings.Join(lines, ","))
}

// newOrderID выдаёт идентификатор заказа так же, как его выдаёт клиент: uuid v7,
// монотонный — клиентский первичный ключ не разносит вставки по индексу.
func newOrderID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("выдать идентификатор заказа: %v", err)
	}

	return id
}

// placeOrder оформляет заказ от имени покупателя и возвращает ответ площадки как есть.
//
// Покупатель — заголовок `User-Id`: это не удостоверение, а самопровозглашённое значение,
// которому площадка верит по условию задания.
func (p *platform) placeOrder(t *testing.T, customer uuid.UUID, body string) response {
	t.Helper()

	return p.asCustomer(t, customer, http.MethodPost, ordersPath, body)
}

// order запрашивает заказ от имени покупателя.
func (p *platform) order(t *testing.T, customer, order uuid.UUID) response {
	t.Helper()

	return p.asCustomer(t, customer, http.MethodGet, ordersPath+"/"+order.String(), "")
}

// asCustomer выполняет запрос от имени покупателя.
func (p *platform) asCustomer(t *testing.T, customer uuid.UUID, method, path, body string) response {
	t.Helper()

	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, p.srv.URL+path, payload)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Id", customer.String())

	return p.send(t, req)
}

// asOrder разбирает ответ как заказ покупателя и проверяет обещанный контрактом код.
func asOrder(t *testing.T, resp response, status int) customerOrder {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("площадка ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var order customerOrder
	if err := json.Unmarshal(resp.Body, &order); err != nil {
		t.Fatalf("разобрать заказ: %v", err)
	}

	return order
}

// changeCatalogPrice меняет цену товара в каталоге площадки.
//
// Правка идёт прямо в базе, хотя каталог умеет приезжать синхронизацией: это тесты
// заказа, и готовить их состояние выгрузкой значило бы проверять правила заказа через
// правила синхронизации — упавший тест перестал бы отвечать на вопрос, что именно
// сломано. Что заказ не меняется от настоящей выгрузки, проверяет тест с живой коробкой.
func (p *platform) changeCatalogPrice(t *testing.T, product uuid.UUID, name string, price int64) {
	t.Helper()

	_, err := p.db.ExecContext(t.Context(),
		`UPDATE platform.products SET name = $2, price = $3 WHERE id = $1`, product, name, price)
	if err != nil {
		t.Fatalf("изменить каталог: %v", err)
	}
}

// storedOrderStatus читает состояние заказа прямо из хранилища площадки.
func (p *platform) storedOrderStatus(t *testing.T, order uuid.UUID) string {
	t.Helper()

	var status string
	if err := p.db.QueryRowContext(t.Context(),
		`SELECT status FROM platform.orders WHERE id = $1`, order).Scan(&status); err != nil {
		t.Fatalf("прочитать состояние заказа %s: %v", order, err)
	}

	return status
}

func TestOrderAcceptedByTheRestaurantWaitsForItsDecision(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	resp := platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2}))

	order := asOrder(t, resp, http.StatusCreated)
	if order.Status != "under_review" {
		t.Errorf("статус заказа %q, ожидали \"under_review\": заведение приняло заказ на рассмотрение",
			order.Status)
	}
	if order.ID != id {
		t.Errorf("идентификатор заказа %s, ожидали выданный клиентом %s", order.ID, id)
	}
	if order.CustomerID != customer {
		t.Errorf("покупатель заказа %s, ожидали %s из заголовка User-Id", order.CustomerID, customer)
	}
}

func TestTotalIsCountedByThePlatformAndNotTakenFromTheClient(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})
	latte := platform.addProduct(t, registered.ID, menuItem{Name: "Латте", Price: 29900})

	id := newOrderID(t)
	// Клиент присылает свою сумму — площадка не должна её увидеть даже значением: поля
	// `total` в схеме оформления нет вовсе, и присланное просто не читается.
	body := fmt.Sprintf(`{"order_id":%q,"restaurant_id":%q,"address":%q,"total":1,"items":[
		{"product_id":%q,"quantity":2},{"product_id":%q,"quantity":1}]}`,
		id, registered.ID, "Москва, Лесная 7, кв. 12", croissant, latte)

	order := asOrder(t, platform.placeOrder(t, uuid.New(), body), http.StatusCreated)

	if want := int64(2*45000 + 29900); order.Total != want {
		t.Errorf("сумма заказа %d, ожидали посчитанную площадкой %d", order.Total, want)
	}
}

func TestOrderKeepsItemNamesAndPricesTheCustomerSawWhenThePlatformCatalogChanges(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	placed := asOrder(t, platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2})), http.StatusCreated)

	platform.changeCatalogPrice(t, croissant, "Круассан с миндалём", 90000)

	order := asOrder(t, platform.order(t, customer, id), http.StatusOK)
	if len(order.Items) != 1 {
		t.Fatalf("в заказе %d позиций, ожидали одну", len(order.Items))
	}
	if order.Items[0].Price != 45000 || order.Items[0].Name != "Круассан" {
		t.Errorf("позиция заказа %q по %d: обновление каталога переписало историю покупателя",
			order.Items[0].Name, order.Items[0].Price)
	}
	if order.Total != placed.Total {
		t.Errorf("сумма заказа стала %d, была %d: сумма посчитана один раз — при оформлении",
			order.Total, placed.Total)
	}
}

func TestOrderIsStoredAsPendingBeforeTheRestaurantIsAsked(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.observePlatform(func(order uuid.UUID) string {
		return platform.storedOrderStatus(t, order)
	})

	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	asOrder(t, platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusCreated)

	deliveries := bakery.delivered()
	if len(deliveries) != 1 {
		t.Fatalf("заведению доставлено %d заказов, ожидали один", len(deliveries))
	}
	if deliveries[0].StatusOnPlatform != "pending" {
		t.Errorf("в момент обращения к заведению заказ был в состоянии %q, ожидали \"pending\": "+
			"площадка, упавшая после вызова, потеряла бы заказ, который заведение уже готовит",
			deliveries[0].StatusOnPlatform)
	}
}

func TestRestaurantReceivesTheOrderWithItsIdempotencyKeyAndAttemptNumber(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	asOrder(t, platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusCreated)

	deliveries := bakery.delivered()
	if len(deliveries) != 1 {
		t.Fatalf("заведению доставлено %d заказов, ожидали один", len(deliveries))
	}
	if deliveries[0].Key != id.String() {
		t.Errorf("ключ идемпотентности %q, ожидали идентификатор заказа %s: по нему заведение "+
			"узнаёт повторную отправку", deliveries[0].Key, id)
	}
	if deliveries[0].Attempt != "1" {
		t.Errorf("номер попытки %q, ожидали \"1\": первая доставка не должна выглядеть повтором",
			deliveries[0].Attempt)
	}
}

func TestRestaurantReceivesTheOrderInItsOwnIdentifiersAndPrices(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	asOrder(t, platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 3})), http.StatusCreated)

	deliveries := bakery.delivered()
	if len(deliveries) != 1 {
		t.Fatalf("заведению доставлено %d заказов, ожидали один", len(deliveries))
	}

	arrived := deliveries[0].Order
	if arrived.OrderID != id {
		t.Errorf("заведению уехал заказ %s, ожидали %s", arrived.OrderID, id)
	}
	if arrived.Address != "Москва, Лесная 7, кв. 12" {
		t.Errorf("адрес доставки %q: заведению везти заказ по нему", arrived.Address)
	}
	if len(arrived.Items) != 1 {
		t.Fatalf("в переданном заказе %d позиций, ожидали одну", len(arrived.Items))
	}
	// Позиция называется тем идентификатором, которым её знает само заведение:
	// собственный идентификатор товара выдала площадка, и заведению он ничего не говорит.
	if arrived.Items[0].ExternalID != croissant.String() {
		t.Errorf("позиция уехала под идентификатором %q, ожидали внешний %s",
			arrived.Items[0].ExternalID, croissant)
	}
	if arrived.Items[0].Price != 45000 || arrived.Items[0].Quantity != 3 {
		t.Errorf("позиция уехала по %d за %d штук, ожидали 45000 за 3: заведение сверяет цену "+
			"с той, что показали покупателю", arrived.Items[0].Price, arrived.Items[0].Quantity)
	}
	if arrived.Total != 3*45000 {
		t.Errorf("сумма переданного заказа %d, ожидали посчитанную площадкой %d",
			arrived.Total, 3*45000)
	}
}

func TestCustomerSeesTheStatusItemsAndTotalOfHisOrder(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2}))

	order := asOrder(t, platform.order(t, customer, id), http.StatusOK)

	switch {
	case order.Status != "under_review":
		t.Errorf("статус заказа %q, ожидали \"under_review\"", order.Status)
	case order.Total != 2*45000:
		t.Errorf("сумма заказа %d, ожидали 90000", order.Total)
	case len(order.Items) != 1 || order.Items[0].ProductID != croissant:
		t.Errorf("состав заказа %+v, ожидали заказанный круассан", order.Items)
	case order.Address != "Москва, Лесная 7, кв. 12":
		t.Errorf("адрес заказа %q", order.Address)
	}
}

func TestOrderOfAnotherCustomerIsAsGoodAsNonExistent(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1}))

	refusal := asRefusal(t, platform.order(t, uuid.New(), id), http.StatusNotFound)
	if refusal.Code != "order_not_found" {
		t.Errorf("код отказа %q, ожидали \"order_not_found\": чужой заказ неотличим от несуществующего",
			refusal.Code)
	}
}

func TestOrderIsNotPlacedWhenTheRestaurantDoesNotAnswerAtAll(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	// Заведение подключено по адресу, на котором никто не слушает: так выглядит упавшая
	// коробка со стороны площадки.
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	refusal := asRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusBadGateway)

	if refusal.Code != "restaurant_unavailable" {
		t.Errorf("код отказа %q, ожидали \"restaurant_unavailable\"", refusal.Code)
	}
	if status := platform.storedOrderStatus(t, id); status != "pending" {
		t.Errorf("заказ остался в состоянии %q, ожидали \"pending\": заведение о нём не узнало", status)
	}
}

func TestOrderIsNotPlacedWhenTheRestaurantDoesNotAnswerInTime(t *testing.T) {
	t.Parallel()

	// Площадка ждёт заведение не дольше, чем нужно тесту: настоящий предел ждать вместе
	// с ней незачем — правило проверяется тем, что предел вообще есть.
	platform := newPlatformWaitingForBox(t, 200*time.Millisecond)
	silent := newBox(t)
	// Заведение приняло соединение и молчит, пока площадка не бросит ждать. Молчание
	// ограничено сверху: заглушка обязана отпустить обработчик, иначе она пережила бы сам
	// тест — а проверяется здесь предел ожидания площадки, а не выдержка заглушки.
	silent.answerWith(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(silence):
		}
	})

	registered := platform.connect(t, "Пекарня «Батон»", silent)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	id := newOrderID(t)
	refusal := asRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusGatewayTimeout)

	if refusal.Code != "restaurant_unavailable" {
		t.Errorf("код отказа %q, ожидали \"restaurant_unavailable\"", refusal.Code)
	}
}

func TestOrderInARestaurantThatDoesNotWorkIsNotPlaced(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	closed := platform.closedRestaurant(t, "Пекарня «Батон»")
	croissant := platform.addProduct(t, closed, menuItem{Name: "Круассан", Price: 45000})

	for _, restaurant := range []uuid.UUID{closed, uuid.New()} {
		refusal := asRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), restaurant,
			"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusNotFound)

		if refusal.Code != "restaurant_not_found" {
			t.Errorf("код отказа %q, ожидали \"restaurant_not_found\"", refusal.Code)
		}
	}
}

func TestOrderOfAProductThatIsNotInThisRestaurantMenuIsNotPlaced(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	// Товар соседнего заведения: он существует, но заказать его здесь нельзя.
	neighbour := platform.openRestaurant(t, "Кофейня «Бариста»")
	latte := platform.addProduct(t, neighbour, menuItem{Name: "Латте", Price: 29900})

	for _, product := range []uuid.UUID{latte, uuid.New()} {
		refusal := asRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
			"Москва, Лесная 7, кв. 12", ordered{product: product, quantity: 1})), http.StatusNotFound)

		if refusal.Code != "product_not_found" {
			t.Errorf("код отказа %q, ожидали \"product_not_found\"", refusal.Code)
		}
	}

	if delivered := bakery.delivered(); len(delivered) != 0 {
		t.Errorf("заведению доставлено %d заказов: заказ, который площадка не собрала, "+
			"передавать нечем", len(delivered))
	}
}

func TestOrderWithoutAddressOrItemsIsNotPlaced(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	bodies := map[string]string{
		"без адреса": orderBody(newOrderID(t), registered.ID, "   ",
			ordered{product: croissant, quantity: 1}),
		"без позиций": orderBody(newOrderID(t), registered.ID, "Москва, Лесная 7, кв. 12"),
		"без адреса вовсе": fmt.Sprintf(`{"order_id":%q,"restaurant_id":%q,"items":[{"product_id":%q,"quantity":1}]}`,
			newOrderID(t), registered.ID, croissant),
		"без идентификатора заказа": fmt.Sprintf(`{"restaurant_id":%q,"address":"Москва","items":[{"product_id":%q,"quantity":1}]}`,
			registered.ID, croissant),
	}

	for name, body := range bodies {
		refusal := asRefusal(t, platform.placeOrder(t, uuid.New(), body), http.StatusBadRequest)
		if refusal.Code != "validation_failed" {
			t.Errorf("заказ %s: код отказа %q, ожидали \"validation_failed\"", name, refusal.Code)
		}
	}
}

func TestOrderWithoutACustomerIsNotPlaced(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	// Заголовка `User-Id` нет вовсе: заказ, который некому показать, площадка не заводит.
	resp := platform.post(t, ordersPath, []byte(orderBody(newOrderID(t), registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})))

	if refusal := asRefusal(t, resp, http.StatusBadRequest); refusal.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали \"validation_failed\"", refusal.Code)
	}
}

// Заглушкой этот путь не проверить: она подтвердила бы только саму себя. По ту сторону
// контракта здесь стоит настоящая коробка — та же, что и в запуске, — и заказ, собранный
// площадкой, обязан быть принят ею по-настоящему: с теми ценами, которые коробка сама же
// и отдала в меню.

// operatorOrder — входящий заказ глазами оператора коробки.
type operatorOrder struct {
	ID              uuid.UUID `json:"id"`
	PlatformOrderID uuid.UUID `json:"platform_order_id"`
	Status          string    `json:"status"`
	Address         string    `json:"address"`
	Total           int64     `json:"total"`
	Items           []struct {
		MenuItemID uuid.UUID `json:"menu_item_id"`
		Name       string    `json:"name"`
		Price      int64     `json:"price"`
		Quantity   int       `json:"quantity"`
	} `json:"items"`
}

// orders читает входящие заказы рабочим местом оператора коробки.
func (b *operatorBox) orders(t *testing.T) []operatorOrder {
	t.Helper()

	resp := b.do(t, http.MethodGet, "/api/operator/v1/orders", "")
	if resp.Status != http.StatusOK {
		t.Fatalf("список заказов коробки вернул %d, ожидали 200: %s", resp.Status, resp.Body)
	}

	var page struct {
		Items []operatorOrder `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("разобрать список заказов коробки: %v", err)
	}

	return page.Items
}

func TestOrderPlacedOnThePlatformArrivesInTheRestaurantBoxAsTheOperatorSeesIt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	// Каталог приезжает выгрузкой из коробки: цена, по которой площадка покажет позицию
	// покупателю, — это цена самой коробки, и заказ уедет обратно с ней же.
	croissant := box.addItem(t, "Круассан", 45000, 10)
	platform.refreshCatalog(t, registered.Key)
	card := platformCardOf(t, platform, registered.ID, croissant.ID)

	customer, id := uuid.New(), newOrderID(t)
	order := asOrder(t, platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: card, quantity: 2})), http.StatusCreated)

	if order.Status != "under_review" {
		t.Errorf("статус заказа на площадке %q, ожидали \"under_review\"", order.Status)
	}

	incoming := box.orders(t)
	if len(incoming) != 1 {
		t.Fatalf("в коробке %d заказов, ожидали один", len(incoming))
	}

	switch {
	case incoming[0].PlatformOrderID != id:
		t.Errorf("коробка ссылается на заказ площадки %s, ожидали %s", incoming[0].PlatformOrderID, id)
	case incoming[0].Status != "under_review":
		t.Errorf("статус заказа в коробке %q, ожидали \"under_review\"", incoming[0].Status)
	case incoming[0].Total != order.Total:
		t.Errorf("коробка посчитала %d, площадка показала покупателю %d: суммы обязаны сойтись",
			incoming[0].Total, order.Total)
	case incoming[0].Address != "Москва, Лесная 7, кв. 12":
		t.Errorf("адрес доставки в коробке %q", incoming[0].Address)
	case len(incoming[0].Items) != 1 || incoming[0].Items[0].MenuItemID != croissant.ID:
		t.Errorf("состав заказа в коробке %+v, ожидали позицию %s", incoming[0].Items, croissant.ID)
	case incoming[0].Items[0].Quantity != 2:
		t.Errorf("коробка приняла %d единиц, заказывали 2", incoming[0].Items[0].Quantity)
	}
}
