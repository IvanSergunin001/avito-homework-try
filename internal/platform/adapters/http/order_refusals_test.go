// Отказы в оформлении заказа и идемпотентность повтора — через тот же шов, что и всё
// остальное: настоящий роутер площадки, настоящий PostgreSQL, заведение по ту сторону
// контракта. Заведение здесь чаще заглушка, чем коробка: отказать «позиции не хватает» и
// «цена не та» ей велит тест, и ответ приходит ровно тот, который описан контрактом.
//
// Там, где отказ должен быть настоящим, стоит настоящая коробка: стоп-лист и подорожание
// в ней ставит оператор, а площадка узнаёт о них так же, как узнала бы в жизни.
package http_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	// catalogCatchUpTimeout — сколько тест ждёт, пока витрина догонит меню заведения.
	// Внеочередную выгрузку площадка затевает сама и мимо покупателя: ответ он получает
	// раньше, чем она заканчивается, поэтому увидеть её результат можно только дождавшись.
	catalogCatchUpTimeout = 10 * time.Second
	// catalogCatchUpPoll — как часто переспрашиваем витрину.
	catalogCatchUpPoll = 50 * time.Millisecond
)

// boxRefusal — отказ заведения в схеме партнёрского контракта: причина, позиция и её
// актуальная цена.
type boxRefusal struct {
	// Code — машиночитаемая причина отказа из перечня контракта.
	Code string
	// ProductExternalID — позиция, из-за которой отказан заказ, в идентификаторах
	// заведения; пустая — отказ не про позицию.
	ProductExternalID string
	// CurrentPrice — актуальная цена позиции; nil — цену заведение не назвало.
	CurrentPrice *int64
	// Detail — пояснение заведения человеку.
	Detail string
}

// write отдаёт отказ так, как отдаёт его заведение: `409` и problem+json.
func (r boxRefusal) write(w http.ResponseWriter) {
	document := map[string]any{
		"type":   "about:blank",
		"title":  "Заказ не принят",
		"status": http.StatusConflict,
		"code":   r.Code,
		"detail": r.Detail,
	}
	if r.ProductExternalID != "" {
		document["product_external_id"] = r.ProductExternalID
	}
	if r.CurrentPrice != nil {
		document["current_price"] = *r.CurrentPrice
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(document)
}

// refuseOrders заставляет заведение отказывать в каждом переданном заказе. Меню оно при
// этом отдаёт по-прежнему: за меню площадка и приходит следом, узнав, что цена разошлась.
func (b *box) refuseOrders(refusal boxRefusal) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refusal = &refusal
}

// orderRefusal — отказ в оформлении заказа глазами покупателя: к общим полям problem+json
// добавлены товар, из-за которого отказан заказ, и его актуальная цена.
type orderRefusal struct {
	problemDocument
	ProductID    *uuid.UUID `json:"product_id"`
	CurrentPrice *int64     `json:"current_price"`
}

// asOrderRefusal разбирает ответ как отказ в оформлении заказа.
func asOrderRefusal(t *testing.T, resp response, status int) orderRefusal {
	t.Helper()

	asRefusal(t, resp, status)

	var refusal orderRefusal
	if err := json.Unmarshal(resp.Body, &refusal); err != nil {
		t.Fatalf("разобрать отказ: %v", err)
	}

	return refusal
}

// ordersOf считает заказы покупателя в хранилище площадки: правило «второй заказ не
// создаётся» проверяется по строкам таблицы, а не по ответу — ответ мог бы скрыть второй
// заказ, отдав первый.
func (p *platform) ordersOf(t *testing.T, customer uuid.UUID) int {
	t.Helper()

	var count int
	if err := p.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM platform.orders WHERE customer_id = $1`, customer).Scan(&count); err != nil {
		t.Fatalf("посчитать заказы покупателя %s: %v", customer, err)
	}

	return count
}

// waitForShowcasePrice ждёт, пока цена позиции на витрине станет такой, какую отдаёт
// заведение. Ждать приходится потому, что внеочередную выгрузку площадка ведёт мимо
// покупателя: его ответ не должен ждать, пока заведение отдаст меню целиком.
func (p *platform) waitForShowcasePrice(t *testing.T, restaurant uuid.UUID, externalID string, want int64) int64 {
	t.Helper()

	deadline := time.Now().Add(catalogCatchUpTimeout)
	for {
		position, known := p.storedPosition(t, restaurant, externalID)
		if known && position.Price == want {
			return position.Price
		}
		if time.Now().After(deadline) {
			return position.Price
		}

		time.Sleep(catalogCatchUpPoll)
	}
}

func TestItemTheRestaurantHasRunOutOfIsRefusedToTheCustomerAsOutOfStock(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})
	bakery.refuseOrders(boxRefusal{
		Code:              "out_of_stock",
		ProductExternalID: croissant.String(),
		Detail:            "заказано 2, в наличии 1",
	})

	customer, id := uuid.New(), newOrderID(t)
	refusal := asOrderRefusal(t, platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2})), http.StatusConflict)

	if refusal.Code != "out_of_stock" {
		t.Errorf("код отказа %q, ожидали \"out_of_stock\"", refusal.Code)
	}
	// Товар — в идентификаторах площадки, а не заведения: покупатель знает свою корзину,
	// и внешнего идентификатора позиции он не видел никогда.
	if refusal.ProductID == nil || *refusal.ProductID != croissant {
		t.Errorf("товар в отказе %v, ожидали %s: клиенту показывать строку корзины",
			refusal.ProductID, croissant)
	}
}

func TestPriceChangedRefusalCarriesThePriceTheRestaurantAsksFor(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	raised := int64(90000)
	bakery.refuseOrders(boxRefusal{
		Code:              "price_changed",
		ProductExternalID: croissant.String(),
		CurrentPrice:      &raised,
		Detail:            "цена изменилась: площадка показала 45000, у заведения 90000",
	})

	refusal := asOrderRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusConflict)

	if refusal.Code != "price_changed" {
		t.Errorf("код отказа %q, ожидали \"price_changed\"", refusal.Code)
	}
	// Новая цена приходит тем же отказом: без неё покупателю осталось бы пересобирать
	// корзину, чтобы узнать, во сколько ему теперь обойдётся тот же заказ.
	if refusal.CurrentPrice == nil || *refusal.CurrentPrice != raised {
		t.Errorf("актуальная цена в отказе %v, ожидали %d", refusal.CurrentPrice, raised)
	}
	if refusal.ProductID == nil || *refusal.ProductID != croissant {
		t.Errorf("товар в отказе %v, ожидали %s", refusal.ProductID, croissant)
	}
}

func TestUnknownItemAndClosedRestaurantReachTheCustomerAsTheirOwnReasons(t *testing.T) {
	t.Parallel()

	// Оба отказа приходят от заведения и оба — `409`: различает их машиночитаемый код, а
	// не код ответа, и клиент выбирает по нему ветку поведения программно.
	refusals := map[string]struct {
		refusal boxRefusal
		product bool
	}{
		"unknown_product": {
			refusal: boxRefusal{Code: "unknown_product", Detail: "такой позиции в меню заведения нет"},
			product: true,
		},
		"restaurant_closed": {
			refusal: boxRefusal{Code: "restaurant_closed", Detail: "заведение сейчас закрыто"},
			product: false,
		},
	}

	for code, expected := range refusals {
		platform := newPlatform(t)
		bakery := newBox(t)
		registered := platform.connect(t, "Пекарня «Батон»", bakery)
		croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

		refusal := expected.refusal
		if expected.product {
			refusal.ProductExternalID = croissant.String()
		}
		bakery.refuseOrders(refusal)

		answer := asOrderRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
			"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusConflict)

		if answer.Code != code {
			t.Errorf("код отказа %q, ожидали %q", answer.Code, code)
		}
		// У закрытого заведения товара в отказе нет: дело не в позиции, и показывать
		// покупателю строку корзины не за что.
		if expected.product != (answer.ProductID != nil) {
			t.Errorf("отказ %q пришёл с товаром %v, ожидали наличие товара %t",
				code, answer.ProductID, expected.product)
		}
	}
}

func TestRefusalTheContractDoesNotDescribeIsAsGoodAsASilentRestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	broken := newBox(t)
	// Заведение отвечает `409` с причиной, которой в приёме заказа взяться неоткуда:
	// придумать по ней покупателю нечего, и выдавать её за объяснение отказа нельзя.
	broken.answerWith(func(w http.ResponseWriter, _ *http.Request) {
		boxRefusal{Code: "order_not_cancellable", Detail: "заказ уже нельзя отменить"}.write(w)
	})

	registered := platform.connect(t, "Пекарня «Батон»", broken)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	refusal := asOrderRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})), http.StatusBadGateway)

	if refusal.Code != "restaurant_unavailable" {
		t.Errorf("код отказа %q, ожидали \"restaurant_unavailable\"", refusal.Code)
	}
}

func TestRepeatedPlacementOfTheSameOrderGivesBackTheOrderAndPlacesNoSecondOne(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	body := orderBody(id, registered.ID, "Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2})

	placed := asOrder(t, platform.placeOrder(t, customer, body), http.StatusCreated)
	// Повтор — это второе нажатие «Заказать» тем же клиентом: тот же идентификатор, тот
	// же состав. Ответ `200`, а не `201`: заказ не создан, а отдан.
	repeated := asOrder(t, platform.placeOrder(t, customer, body), http.StatusOK)

	switch {
	case repeated.ID != placed.ID:
		t.Errorf("повтор вернул заказ %s, ожидали уже созданный %s", repeated.ID, placed.ID)
	case repeated.Total != placed.Total || repeated.Status != placed.Status:
		t.Errorf("повтор вернул заказ на %d в состоянии %q, ожидали %d и %q",
			repeated.Total, repeated.Status, placed.Total, placed.Status)
	case repeated.CreatedAt != placed.CreatedAt:
		t.Errorf("повтор вернул заказ, оформленный %s, ожидали %s: это тот же самый заказ",
			repeated.CreatedAt, placed.CreatedAt)
	}

	if orders := platform.ordersOf(t, customer); orders != 1 {
		t.Errorf("у покупателя %d заказов, ожидали один: повтор не заводит второй", orders)
	}
	if delivered := bakery.delivered(); len(delivered) != 1 {
		t.Errorf("заведению доставлено %d заказов, ожидали один: заказ, который у площадки уже есть, "+
			"передавать второй раз нечего", len(delivered))
	}
}

func TestRetryAfterTheRestaurantFailedToAnswerInTimePlacesNoSecondOrder(t *testing.T) {
	t.Parallel()

	platform := newPlatformWaitingForBox(t, 200*time.Millisecond)
	silent := newBox(t)
	silent.answerWith(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(silence):
		}
	})

	registered := platform.connect(t, "Пекарня «Батон»", silent)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	customer, id := uuid.New(), newOrderID(t)
	body := orderBody(id, registered.ID, "Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 1})

	asRefusal(t, platform.placeOrder(t, customer, body), http.StatusGatewayTimeout)

	// Ретрай по таймауту приходит с тем же идентификатором: заказ у площадки уже есть, и
	// второго она не заводит — покупатель получает свой, каким он есть.
	repeated := asOrder(t, platform.placeOrder(t, customer, body), http.StatusOK)
	if repeated.ID != id {
		t.Errorf("ретрай вернул заказ %s, ожидали %s", repeated.ID, id)
	}
	if repeated.Status != "pending" {
		t.Errorf("состояние заказа %q, ожидали \"pending\": заведение о заказе так и не отозвалось",
			repeated.Status)
	}
	if orders := platform.ordersOf(t, customer); orders != 1 {
		t.Errorf("у покупателя %d заказов, ожидали один: ретрай не заводит второй", orders)
	}
}

func TestSameOrderIdWithADifferentBasketIsRefusedAsIdempotencyKeyReuse(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})
	latte := platform.addProduct(t, registered.ID, menuItem{Name: "Латте", Price: 29900})

	customer, id := uuid.New(), newOrderID(t)
	address := "Москва, Лесная 7, кв. 12"
	placed := asOrder(t, platform.placeOrder(t, customer, orderBody(id, registered.ID, address,
		ordered{product: croissant, quantity: 2})), http.StatusCreated)

	// Тот же идентификатор, другая корзина: это не повтор, а сломанный клиент — отдать ему
	// молча чужой по смыслу заказ значило бы подтвердить покупку, которой он не делал.
	refusal := asRefusal(t, platform.placeOrder(t, customer, orderBody(id, registered.ID, address,
		ordered{product: latte, quantity: 1})), http.StatusUnprocessableEntity)

	if refusal.Code != "idempotency_key_reuse" {
		t.Errorf("код отказа %q, ожидали \"idempotency_key_reuse\"", refusal.Code)
	}

	kept := asOrder(t, platform.order(t, customer, id), http.StatusOK)
	if len(kept.Items) != 1 || kept.Items[0].ProductID != croissant || kept.Total != placed.Total {
		t.Errorf("заказ стал %+v на %d, ожидали неизменным: отказ не переписывает уже оформленное",
			kept.Items, kept.Total)
	}
	if orders := platform.ordersOf(t, customer); orders != 1 {
		t.Errorf("у покупателя %d заказов, ожидали один", orders)
	}
}

func TestOrderIdTakenByAnotherCustomerTellsNothingAboutTheOrderBehindIt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})

	owner, id := uuid.New(), newOrderID(t)
	address := "Москва, Лесная 7, кв. 12"
	asOrder(t, platform.placeOrder(t, owner, orderBody(id, registered.ID, address,
		ordered{product: croissant, quantity: 2})), http.StatusCreated)

	stranger := uuid.New()
	resp := platform.placeOrder(t, stranger, orderBody(id, registered.ID, "Москва, Тверская 1",
		ordered{product: croissant, quantity: 1}))
	refusal := asRefusal(t, resp, http.StatusConflict)

	if refusal.Code != "idempotency_key_reuse" {
		t.Errorf("код отказа %q, ожидали \"idempotency_key_reuse\"", refusal.Code)
	}
	// В отказе нет ничего о чужом заказе: ни покупателя, ни адреса, ни состава. Иначе
	// перебор идентификаторов рассказал бы, что заказывают другие.
	for name, secret := range map[string]string{
		"покупатель": owner.String(),
		"адрес":      address,
		"товар":      croissant.String(),
	} {
		if strings.Contains(string(resp.Body), secret) {
			t.Errorf("в отказе виден %s чужого заказа: %s", name, resp.Body)
		}
	}

	if orders := platform.ordersOf(t, stranger); orders != 0 {
		t.Errorf("у чужого покупателя %d заказов, ожидали ни одного", orders)
	}
	if delivered := bakery.delivered(); len(delivered) != 1 {
		t.Errorf("заведению доставлено %d заказов, ожидали один", len(delivered))
	}
}

// Дальше по ту сторону контракта стоит настоящая коробка — та же, что и в запуске.
// Заглушкой эти правила не проверить: отказ ей велит тест, и подтвердила бы она только
// саму себя, тогда как здесь отказ рождается из стоп-листа и ценника, которые ставит
// оператор заведения.

func TestItemTheOperatorPutOnTheStopListCanNoLongerBeOrdered(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	croissant := box.addItem(t, "Круассан", 45000, 10)
	platform.refreshCatalog(t, registered.Key)
	card := platformCardOf(t, platform, registered.ID, croissant.ID)

	// Позиция уходит в стоп-лист после выгрузки: на витрине она ещё продаётся, и
	// покупатель узнаёт правду от заведения — оно источник истины по наличию.
	box.markStopList(t, croissant.ID, true)

	refusal := asOrderRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: card, quantity: 1})), http.StatusConflict)

	if refusal.Code != "out_of_stock" {
		t.Errorf("код отказа %q, ожидали \"out_of_stock\"", refusal.Code)
	}
	if refusal.ProductID == nil || *refusal.ProductID != card {
		t.Errorf("товар в отказе %v, ожидали %s", refusal.ProductID, card)
	}
}

func TestPriceRaisedByTheRestaurantReachesTheCustomerAndThenTheShowcase(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	croissant := box.addItem(t, "Круассан", 45000, 10)
	platform.refreshCatalog(t, registered.Key)
	card := platformCardOf(t, platform, registered.ID, croissant.ID)

	// Заведение подняло цену, а витрина об этом ещё не знает: следующая плановая выгрузка
	// её догонит, но покупатель нажал «Заказать» раньше.
	box.editCard(t, croissant.ID, "Круассан", 90000)

	refusal := asOrderRefusal(t, platform.placeOrder(t, uuid.New(), orderBody(newOrderID(t), registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: card, quantity: 1})), http.StatusConflict)

	switch {
	case refusal.Code != "price_changed":
		t.Errorf("код отказа %q, ожидали \"price_changed\"", refusal.Code)
	case refusal.CurrentPrice == nil || *refusal.CurrentPrice != 90000:
		t.Errorf("актуальная цена в отказе %v, ожидали 90000", refusal.CurrentPrice)
	}

	// Отказ покупателю — это половина ответа: витрина, оставшаяся со старой ценой,
	// отправила бы следующего покупателя за тем же самым отказом. Поэтому площадка идёт
	// за меню сама, не дожидаясь ни планового обхода, ни просьбы заведения.
	if price := platform.waitForShowcasePrice(t, registered.ID, croissant.ID.String(), 90000); price != 90000 {
		t.Errorf("на витрине цена %d, ожидали догнавшую заведение 90000: отказ по цене "+
			"обязан отправить площадку за меню", price)
	}
}

// Заказ, который заведение не взяло, остаётся у площадки записанным: покупатель видит его
// в своей истории, а разбираться с ним — дело следующих слайсов (отмена и уведомления).
func TestOrderRefusedByTheRestaurantStaysWithThePlatformAsTheCustomerLeftIt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	croissant := platform.addProduct(t, registered.ID, menuItem{Name: "Круассан", Price: 45000})
	bakery.refuseOrders(boxRefusal{
		Code:              "out_of_stock",
		ProductExternalID: croissant.String(),
		Detail:            "позиция в стоп-листе",
	})

	customer, id := uuid.New(), newOrderID(t)
	asOrderRefusal(t, platform.placeOrder(t, customer, orderBody(id, registered.ID,
		"Москва, Лесная 7, кв. 12", ordered{product: croissant, quantity: 2})), http.StatusConflict)

	order := asOrder(t, platform.order(t, customer, id), http.StatusOK)
	if order.Status != "pending" {
		t.Errorf("состояние отказанного заказа %q, ожидали \"pending\": заведение его не приняло",
			order.Status)
	}
	if order.Total != 2*45000 {
		t.Errorf("сумма заказа %d, ожидали %d", order.Total, 2*45000)
	}
}
