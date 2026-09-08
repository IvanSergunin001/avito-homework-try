package http_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// pageOfRestaurants — ответ каталога глазами клиента: разбираем его так же, как разбирал бы
// покупательский фронтенд, — по схеме из спецификации, а не по внутренним типам площадки.
type pageOfRestaurants struct {
	Items []restaurantCard `json:"items"`
	Page  struct {
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
		Total  int64 `json:"total"`
	} `json:"page"`
}

// restaurantCard — заведение глазами клиента.
type restaurantCard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// names — названия заведений страницы в том порядке, в каком их отдала площадка.
func (p pageOfRestaurants) names() []string {
	names := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		names = append(names, item.Name)
	}

	return names
}

// catalogPage запрашивает страницу витрины и разбирает её.
func (p *platform) catalogPage(t *testing.T, path string) pageOfRestaurants {
	t.Helper()

	resp := p.get(t, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s вернул %d, ожидали 200: %s", path, resp.Status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var page pageOfRestaurants
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("разобрать страницу каталога: %v", err)
	}

	return page
}

func TestCatalogPageUsesWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	t.Parallel()

	page := newPlatform(t).catalogPage(t, "/api/v1/restaurants")

	if page.Page.Limit != 20 || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта 20 и 0",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestCatalogPageEchoesTheWindowTheClientAskedFor(t *testing.T) {
	t.Parallel()

	page := newPlatform(t).catalogPage(t, "/api/v1/restaurants?limit=5&offset=10")

	if page.Page.Limit != 5 || page.Page.Offset != 10 {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные 5 и 10",
			page.Page.Limit, page.Page.Offset)
	}
}

// Первый тест по шву: витрина площадки, за которой стоит пустая база с накатанными
// миграциями. Ни одного заведения на площадке не зарегистрировано, и клиент получает не
// отсутствие коллекции, а пустую страницу — фронтенду нечего различать в двух видах пустоты.
func TestCatalogWithoutRegisteredRestaurantsIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	t.Parallel()

	page := newPlatform(t).catalogPage(t, "/api/v1/restaurants")

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом каталоге %d записей при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestCatalogRefusesPageLargerThanContractAllows(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t, "/api/v1/restaurants?limit=101", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", document.Status)
	}
}

func TestCatalogRefusesPageSizeThatIsNotANumber(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t, "/api/v1/restaurants?limit=неполный", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

// card запрашивает карточку заведения и разбирает её.
func (p *platform) card(t *testing.T, id uuid.UUID) restaurantCard {
	t.Helper()

	path := "/api/v1/restaurants/" + id.String()

	resp := p.get(t, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s вернул %d, ожидали 200: %s", path, resp.Status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var card restaurantCard
	if err := json.Unmarshal(resp.Body, &card); err != nil {
		t.Fatalf("разобрать карточку заведения: %v", err)
	}

	return card
}

func TestCatalogShowsTheRestaurantsThatWork(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	platform.openRestaurant(t, "Пекарня «Батон»")
	platform.openRestaurant(t, "Кофейня «Пар»")

	page := platform.catalogPage(t, "/api/v1/restaurants")

	got := page.names()
	slices.Sort(got)
	want := []string{"Кофейня «Пар»", "Пекарня «Батон»"}
	if !slices.Equal(got, want) {
		t.Errorf("в витрине %v, ожидали оба работающих заведения %v", got, want)
	}
	if page.Page.Total != 2 {
		t.Errorf("total=%d, ожидали 2: столько заведений работает", page.Page.Total)
	}
}

func TestRestaurantThatStoppedWorkingDisappearsFromTheCatalog(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	platform.openRestaurant(t, "Пекарня «Батон»")
	platform.closedRestaurant(t, "Столовая «Обед»")

	page := platform.catalogPage(t, "/api/v1/restaurants")

	if got := page.names(); !slices.Equal(got, []string{"Пекарня «Батон»"}) {
		t.Errorf("в витрине %v, ожидали одну «Пекарню «Батон»»: неработающему заведению в выдаче не место", got)
	}
	// Счётчик считает то же, что и выдача: иначе клиент листал бы страницы, на которых
	// ничего нет.
	if page.Page.Total != 1 {
		t.Errorf("total=%d, ожидали 1: неработающее заведение не считается", page.Page.Total)
	}
}

func TestCatalogWindowWalksTheWholeListWithoutLosingOrRepeatingRestaurants(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	for _, name := range []string{"Бургерная", "Вареничная", "Пельменная"} {
		platform.openRestaurant(t, name)
	}

	var seen []string
	for _, path := range []string{
		"/api/v1/restaurants?limit=2&offset=0",
		"/api/v1/restaurants?limit=2&offset=2",
	} {
		page := platform.catalogPage(t, path)
		if page.Page.Total != 3 {
			t.Errorf("%s: total=%d, ожидали 3 — общее число не зависит от окна", path, page.Page.Total)
		}
		seen = append(seen, page.names()...)
	}

	slices.Sort(seen)
	want := []string{"Бургерная", "Вареничная", "Пельменная"}
	if !slices.Equal(seen, want) {
		t.Errorf("страницы вместе дали %v, ожидали каждое заведение ровно по разу: %v", seen, want)
	}
}

func TestRestaurantCardIsFoundByTheIdentifierFromTheCatalog(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	id := platform.openRestaurant(t, "Пекарня «Батон»")

	card := platform.card(t, id)

	if card.ID != id.String() {
		t.Errorf("карточка отдала id=%q, ожидали %q", card.ID, id)
	}
	if card.Name != "Пекарня «Батон»" {
		t.Errorf("карточка отдала name=%q, ожидали «Пекарня «Батон»»", card.Name)
	}
}

func TestCardOfRestaurantThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		"/api/v1/restaurants/018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a01", http.StatusNotFound)

	if document.Code != "restaurant_not_found" {
		t.Errorf("код отказа %q, ожидали restaurant_not_found", document.Code)
	}
	if document.Status != http.StatusNotFound {
		t.Errorf("в теле отказа status=%d, ожидали 404", document.Status)
	}
}

// Заведение, исчезнувшее из выдачи, не должно оставаться доступным по прямой ссылке:
// покупателю оно неотличимо от несуществующего — заказать в нём всё равно нельзя.
func TestCardOfRestaurantThatStoppedWorkingIsNotFoundEither(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	id := platform.closedRestaurant(t, "Столовая «Обед»")

	document := platform.refusal(t, "/api/v1/restaurants/"+id.String(), http.StatusNotFound)

	if document.Code != "restaurant_not_found" {
		t.Errorf("код отказа %q, ожидали restaurant_not_found", document.Code)
	}
}

func TestCardRefusesIdentifierThatIsNotAnIdentifier(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t, "/api/v1/restaurants/пекарня", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

// Карточка и меню — разные ресурсы: меню это коллекция неограниченного размера со своей
// пагинацией, и открытие карточки не должно тянуть её целиком.
func TestMenuIsAResourceOfItsOwnAndNotAPartOfTheCard(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	id := platform.openRestaurant(t, "Пекарня «Батон»")

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(platform.get(t, "/api/v1/restaurants/"+id.String()).Body, &fields); err != nil {
		t.Fatalf("разобрать карточку заведения: %v", err)
	}
	for _, field := range []string{"menu", "items", "products"} {
		if _, embedded := fields[field]; embedded {
			t.Errorf("в карточке заведения есть поле %q: меню в карточку не встраивается", field)
		}
	}

	menu := platform.get(t, "/api/v1/restaurants/"+id.String()+"/products?limit=10&offset=0")
	if menu.Status == http.StatusNotFound {
		t.Error("меню заведения не отдаётся собственным адресом со своей пагинацией")
	}
}

// Тело запроса читается в память до того, как хоть одно правило успеет сказать, что
// запрос неверен, поэтому предел на размер стоит раньше разбора.
func TestRequestBodyLargerThanTheLimitIsRefusedWithoutBeingRead(t *testing.T) {
	t.Parallel()

	oversized := bytes.Repeat([]byte("a"), 2<<20)

	document := asRefusal(t,
		newPlatform(t).post(t, "/api/v1/orders", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("в теле отказа status=%d, ожидали 413", document.Status)
	}
}
