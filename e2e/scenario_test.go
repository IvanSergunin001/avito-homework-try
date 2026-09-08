// Сценарий целиком на поднятой системе: каталог доезжает из коробки на витрину, покупатель
// оформляет заказ, оператор ведёт его по кухне, покупатель видит каждый шаг, а второй заказ
// отменяет — и отмена доезжает до заведения.
//
// Тест один, и это осознанно: система поднимается минуту, а правила предметной области
// проверены там, где они живут, — в домене и по швам служб. Разбивать один и тот же путь
// на несколько сквозных тестов значило бы платить за каждый из них поднятием системы,
// ничего при этом не проверяя дополнительно.
package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

const (
	// demoRestaurant — демонстрационное заведение, которое площадка знает сразу после
	// запуска: его заводит seed-миграция, а рядом в Compose стоит его коробка. Ради него
	// система и рабочая с первой же секунды — витрина после запуска не пуста.
	demoRestaurant = "Пекарня «Батон»"
	// demoPartnerKey — партнёрский ключ того же заведения. Он известен всем, кто открывал
	// репозиторий, и годится только для демонстрации: в базе площадки лежит его хеш
	// (seed-миграция), а коробке его выдаёт `compose.yaml`.
	demoPartnerKey = "pk_DEMOKEYDEMOKEYDEMOKEYDEMOK"

	// deliveryAddress — куда покупатель просит привезти заказ.
	deliveryAddress = "Москва, Лесная 7, кв. 12"
	// croissantPrice — цена позиции, которую заводит оператор, в копейках.
	croissantPrice = 45000
	// croissantStock — остаток той же позиции: заказов в сценарии два, и обоим должно
	// хватить.
	croissantStock = 10
)

func TestOrderPassesFromCatalogSyncToDeliveryAndCancellationOnServicesRaisedByCompose(t *testing.T) {
	system := start(t)

	// --- Каталог: меню заводится в коробке и доезжает до площадки ------------------
	//
	// Позиция называется по-своему в каждом прогоне: система поднимается настоящая, и
	// тест не должен зависеть от того, что осталось в базе от прогона, который не убрал
	// за собой.
	croissant := fmt.Sprintf("Круассан %s", uuid.New().String()[:8])
	system.addMenuItem(t, croissant)

	// Заведение зовёт площадку за меню, не дожидаясь очередного цикла синхронизации:
	// иначе тест ждал бы таймера площадки, проверяя не систему, а её расписание.
	system.refreshCatalog(t)

	// --- Витрина: покупатель находит заведение и его меню --------------------------
	restaurant := system.showcase(t).find(t, demoRestaurant)
	card := system.menu(t, restaurant.ID).find(t, croissant)

	if card.Price != croissantPrice {
		t.Fatalf("на витрине цена %d, ожидали заведённую оператором %d", card.Price, croissantPrice)
	}
	if !card.IsAvailable {
		t.Error("позиция, заведённая оператором с остатком, на витрине недоступна")
	}
	if found := system.search(t, croissant).find(t, croissant); found.ID != card.ID {
		t.Errorf("поиск нашёл товар %s, а в меню заведения он же лежит под %s", found.ID, card.ID)
	}

	// --- Заказ: покупатель оформляет его на витрине --------------------------------
	customer := uuid.New()
	placed := system.placeOrder(t, customer, restaurant.ID, card.ID, 2)

	if placed.Status != "under_review" {
		t.Fatalf("статус оформленного заказа %q, ожидали \"under_review\": заведение взяло заказ на рассмотрение",
			placed.Status)
	}
	if want := int64(2 * croissantPrice); placed.Total != want {
		t.Errorf("сумма заказа %d, ожидали посчитанную площадкой %d", placed.Total, want)
	}

	// --- Статусы: оператор ведёт заказ, покупатель видит каждый шаг -----------------
	//
	// Между рабочим местом оператора и экраном покупателя — две службы, сеть и очередь
	// уведомлений коробки; тест смотрит туда же, куда смотрел бы покупатель, и ждёт
	// столько, сколько ждал бы он.
	inBox := system.boxOrder(t, placed.ID)
	for _, step := range []struct{ command, status string }{
		{command: "confirm", status: "confirmed"},
		{command: "start-cooking", status: "cooking"},
		{command: "start-delivery", status: "delivering"},
		{command: "complete", status: "completed"},
	} {
		system.advance(t, inBox.ID, step.command)

		eventually(t, fmt.Sprintf("покупатель видит статус %q после того, как оператор перевёл заказ", step.status),
			func() bool { return system.order(t, customer, placed.ID).Status == step.status })
	}

	// --- Отмена: покупатель передумал, и заведение об этом знает --------------------
	//
	// Заказ второй: первый уже доставлен, а отменить доставленный нельзя — и правильно.
	another := system.placeOrder(t, customer, restaurant.ID, card.ID, 1)

	cancelled := system.cancelOrder(t, customer, another.ID)
	if cancelled.Status != "cancelled" {
		t.Fatalf("статус отменённого заказа %q, ожидали \"cancelled\"", cancelled.Status)
	}
	if cancelled.CancelledBy != "customer" {
		t.Errorf("отмену записали за %q, ожидали \"customer\"", cancelled.CancelledBy)
	}

	eventually(t, "заведение узнало, что покупатель отменил заказ",
		func() bool { return system.boxOrder(t, another.ID).Status == "cancelled" })
}

// --- Коробка заведения: рабочее место оператора ------------------------------------

// menuItemDocument — позиция меню глазами оператора коробки.
type menuItemDocument struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Price int64     `json:"price"`
	Stock int       `json:"stock"`
}

// addMenuItem заводит позицию меню на рабочем месте оператора — так это делает заведение,
// у которого своих систем нет.
func (s *stack) addMenuItem(t *testing.T, name string) menuItemDocument {
	t.Helper()

	body := map[string]any{
		"name":        name,
		"description": "Слоёное тесто",
		"price":       croissantPrice,
		"stock":       croissantStock,
	}

	var added menuItemDocument
	decode(t, s.post(t, s.box+"/api/operator/v1/menu", body, nil), http.StatusCreated, &added)

	return added
}

// boxOrderDocument — заказ глазами оператора коробки.
type boxOrderDocument struct {
	ID              uuid.UUID `json:"id"`
	PlatformOrderID uuid.UUID `json:"platform_order_id"`
	Status          string    `json:"status"`
}

// boxOrder находит у оператора заказ, переданный площадкой. Заказ ищется по идентификатору
// площадки: у коробки он свой, и связь между ними — часть контракта, а не деталь теста.
func (s *stack) boxOrder(t *testing.T, placed uuid.UUID) boxOrderDocument {
	t.Helper()

	var page struct {
		Items []boxOrderDocument `json:"items"`
	}
	decode(t, s.get(t, s.box+"/api/operator/v1/orders", nil), http.StatusOK, &page)

	for _, order := range page.Items {
		if order.PlatformOrderID == placed {
			return order
		}
	}

	t.Fatalf("заказ %s не доехал до заведения: у оператора его нет", placed)

	return boxOrderDocument{}
}

// advance переводит заказ в следующее состояние рабочим местом оператора — так его ведёт
// заведение по кухне.
func (s *stack) advance(t *testing.T, order uuid.UUID, command string) {
	t.Helper()

	endpoint := fmt.Sprintf("%s/api/operator/v1/orders/%s/%s", s.box, order, command)
	if resp := s.post(t, endpoint, nil, nil); resp.Status != http.StatusOK {
		t.Fatalf("оператор перевёл заказ командой %s и получил %d, ожидали 200: %s",
			command, resp.Status, resp.Body)
	}
}

// --- Площадка: партнёрская и клиентская поверхности --------------------------------

// refreshCatalog просит площадку забрать меню заведения вне очереди — партнёрской ручкой
// и с партнёрским ключом, как это делает само заведение.
func (s *stack) refreshCatalog(t *testing.T) {
	t.Helper()

	headers := http.Header{"Authorization": []string{"Bearer " + demoPartnerKey}}

	resp := s.post(t, s.platform+"/api/partner/v1/catalog/refresh", nil, headers)
	if resp.Status != http.StatusAccepted {
		t.Fatalf("внеочередная синхронизация вернула %d, ожидали 202: %s", resp.Status, resp.Body)
	}
}

// restaurantDocument — заведение в витрине покупателя.
type restaurantDocument struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// restaurantPage — страница витрины.
type restaurantPage struct {
	Items []restaurantDocument `json:"items"`
}

// find находит заведение по названию и останавливает тест, если его в витрине нет.
func (p restaurantPage) find(t *testing.T, name string) restaurantDocument {
	t.Helper()

	for _, item := range p.Items {
		if item.Name == name {
			return item
		}
	}

	t.Fatalf("в витрине нет заведения %q", name)

	return restaurantDocument{}
}

// showcase читает список работающих заведений — первый экран покупателя.
func (s *stack) showcase(t *testing.T) restaurantPage {
	t.Helper()

	var page restaurantPage
	decode(t, s.get(t, s.platform+"/api/v1/restaurants", nil), http.StatusOK, &page)

	return page
}

// productDocument — позиция меню в витрине покупателя.
type productDocument struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Price       int64     `json:"price"`
	IsAvailable bool      `json:"is_available"`
}

// productPage — страница меню или поисковой выдачи.
type productPage struct {
	Items []productDocument `json:"items"`
}

// find находит товар по названию и останавливает тест, если его в выдаче нет.
func (p productPage) find(t *testing.T, name string) productDocument {
	t.Helper()

	for _, item := range p.Items {
		if item.Name == name {
			return item
		}
	}

	t.Fatalf("в выдаче нет товара %q", name)

	return productDocument{}
}

// menu читает меню заведения — так его листает покупатель, открывший карточку.
func (s *stack) menu(t *testing.T, restaurant uuid.UUID) productPage {
	t.Helper()

	var page productPage
	decode(t, s.get(t, fmt.Sprintf("%s/api/v1/restaurants/%s/products", s.platform, restaurant), nil),
		http.StatusOK, &page)

	return page
}

// search ищет товар по названию по всем заведениям сразу.
func (s *stack) search(t *testing.T, query string) productPage {
	t.Helper()

	var page productPage
	decode(t, s.get(t, s.platform+"/api/v1/products?q="+url.QueryEscape(query), nil), http.StatusOK, &page)

	return page
}

// orderDocument — заказ глазами покупателя.
type orderDocument struct {
	ID          uuid.UUID `json:"id"`
	Status      string    `json:"status"`
	Total       int64     `json:"total"`
	CancelledBy string    `json:"cancelled_by"`
}

// placeOrder оформляет заказ от имени покупателя. Идентификатор заказа выдаёт клиент —
// uuid v7, он же служит ключом идемпотентности; сумму клиент не присылает, её считает
// площадка.
func (s *stack) placeOrder(t *testing.T, customer, restaurant, product uuid.UUID, quantity int) orderDocument {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("выдать идентификатор заказа: %v", err)
	}

	body := map[string]any{
		"order_id":      id,
		"restaurant_id": restaurant,
		"address":       deliveryAddress,
		"items":         []map[string]any{{"product_id": product, "quantity": quantity}},
	}

	var placed orderDocument
	decode(t, s.post(t, s.platform+"/api/v1/orders", body, customerHeader(customer)),
		http.StatusCreated, &placed)

	return placed
}

// order читает заказ от имени покупателя — так его видно на экране «мои заказы».
func (s *stack) order(t *testing.T, customer, order uuid.UUID) orderDocument {
	t.Helper()

	var read orderDocument
	decode(t, s.get(t, fmt.Sprintf("%s/api/v1/orders/%s", s.platform, order), customerHeader(customer)),
		http.StatusOK, &read)

	return read
}

// cancelOrder отменяет заказ по просьбе покупателя.
func (s *stack) cancelOrder(t *testing.T, customer, order uuid.UUID) orderDocument {
	t.Helper()

	var cancelled orderDocument
	decode(t, s.post(t, fmt.Sprintf("%s/api/v1/orders/%s/cancel", s.platform, order), nil,
		customerHeader(customer)), http.StatusOK, &cancelled)

	return cancelled
}

// customerHeader называет покупателя так же, как это делает веб-приложение: заголовком
// `User-Id`. Это не удостоверение, а самопровозглашённый идентификатор, которому площадка
// верит по условию задания.
func customerHeader(customer uuid.UUID) http.Header {
	return http.Header{"User-Id": []string{customer.String()}}
}
