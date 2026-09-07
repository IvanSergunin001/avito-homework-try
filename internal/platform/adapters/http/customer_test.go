package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	customer "github.com/IvanSergunin001/Test157/api/customer/v1"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// catalog — сгенерированный по спецификации клиент витрины. Тест смотрит на площадку
// теми же глазами, что и покупательский фронтенд: разбирает ответ типами контракта, а
// не своей копией схемы, — разойтись с контрактом такой тест не может.
func (p *platform) catalog(t *testing.T) *customer.ClientWithResponses {
	t.Helper()

	client, err := customer.NewClientWithResponses(p.srv.URL+platformhttp.CustomerAPIPrefix,
		customer.WithHTTPClient(p.srv.Client()))
	if err != nil {
		t.Fatalf("собрать клиент витрины: %v", err)
	}

	return client
}

// restaurantPage запрашивает страницу каталога и требует, чтобы ответ был успешным.
func (p *platform) restaurantPage(
	t *testing.T, params *customer.ListRestaurantsParams,
) customer.RestaurantPage {
	t.Helper()

	resp, err := p.catalog(t).ListRestaurantsWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить каталог: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("каталог вернул %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.JSON200
}

func TestCatalogPageUsesWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	t.Parallel()

	page := newPlatform(t).restaurantPage(t, &customer.ListRestaurantsParams{})

	if page.Page.Limit != platformhttp.CustomerPageDefault || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта %d и 0",
			page.Page.Limit, page.Page.Offset, platformhttp.CustomerPageDefault)
	}
}

func TestCatalogPageEchoesTheWindowTheClientAskedFor(t *testing.T) {
	t.Parallel()

	limit, offset := 5, 10

	page := newPlatform(t).restaurantPage(t,
		&customer.ListRestaurantsParams{Limit: &limit, Offset: &offset})

	if page.Page.Limit != limit || page.Page.Offset != offset {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные %d и %d",
			page.Page.Limit, page.Page.Offset, limit, offset)
	}
}

// Первый тест по шву: витрина площадки, за которой стоит пустая база с накатанными
// миграциями. Ни одного заведения на площадке не зарегистрировано, и клиент получает не
// отсутствие коллекции, а пустую страницу — фронтенду нечего различать в двух видах пустоты.
func TestCatalogWithoutRegisteredRestaurantsIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	t.Parallel()

	page := newPlatform(t).restaurantPage(t, &customer.ListRestaurantsParams{})

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

	limit := platformhttp.CustomerPageMax + 1

	resp, err := newPlatform(t).catalog(t).ListRestaurantsWithResponse(context.Background(),
		&customer.ListRestaurantsParams{Limit: &limit})
	if err != nil {
		t.Fatalf("запросить каталог: %v", err)
	}
	if resp.ApplicationproblemJSON400 == nil {
		t.Fatalf("каталог вернул %d, ожидали 400: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if code := resp.ApplicationproblemJSON400.Code; code != customer.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", code, customer.ValidationFailed)
	}
	if status := resp.ApplicationproblemJSON400.Status; status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", status)
	}
}

// Нечисловой `limit` клиентом не отправить — его тип в контракте целое, — поэтому такой
// запрос идёт сырым: он проверяет разбор параметров, который живёт до обработчика.
func TestCatalogRefusesPageSizeThatIsNotANumber(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		platformhttp.CustomerAPIPrefix+"/restaurants?limit=неполный", http.StatusBadRequest)

	if document.Code != string(customer.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
}

// TestOperationDescribedByContractButNotYetImplementedAnswersNotImplemented закрепляет
// решение: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе.
//
// Запрос идёт сгенерированным клиентом: `501` контрактом не описан — его в ответах
// операции нет, — поэтому типизированного поля под него у клиента тоже нет, и тело
// разбирается как отказ вручную.
func TestOperationDescribedByContractButNotYetImplementedAnswersNotImplemented(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp, err := platform.catalog(t).GetOrderWithResponse(context.Background(),
		uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a03"),
		&customer.GetOrderParams{UserId: uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a04")})
	if err != nil {
		t.Fatalf("запросить заказ: %v", err)
	}

	document := asRefusal(t, response{
		Status:      resp.HTTPResponse.StatusCode,
		ContentType: resp.HTTPResponse.Header.Get("Content-Type"),
		Body:        resp.Body,
	}, http.StatusNotImplemented)

	if document.Code != string(customer.NotImplemented) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.NotImplemented)
	}
}

// names — названия заведений страницы в том порядке, в каком их отдала площадка.
func names(page customer.RestaurantPage) []string {
	got := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		got = append(got, item.Name)
	}

	return got
}

// card запрашивает карточку заведения и требует, чтобы ответ был успешным.
func (p *platform) card(t *testing.T, id uuid.UUID) customer.Restaurant {
	t.Helper()

	resp, err := p.catalog(t).GetRestaurantWithResponse(context.Background(), id)
	if err != nil {
		t.Fatalf("запросить карточку заведения: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("карточка вернула %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.JSON200
}

// missingCard требует, чтобы карточка заведения не нашлась, и возвращает отказ.
func (p *platform) missingCard(t *testing.T, id uuid.UUID) customer.NotFound {
	t.Helper()

	resp, err := p.catalog(t).GetRestaurantWithResponse(context.Background(), id)
	if err != nil {
		t.Fatalf("запросить карточку заведения: %v", err)
	}
	if resp.ApplicationproblemJSON404 == nil {
		t.Fatalf("карточка вернула %d, ожидали 404: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.ApplicationproblemJSON404
}

func TestCatalogShowsTheRestaurantsThatWork(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	platform.openRestaurant(t, "Пекарня «Батон»")
	platform.openRestaurant(t, "Кофейня «Пар»")

	page := platform.restaurantPage(t, &customer.ListRestaurantsParams{})

	got := names(page)
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

	page := platform.restaurantPage(t, &customer.ListRestaurantsParams{})

	if got := names(page); !slices.Equal(got, []string{"Пекарня «Батон»"}) {
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

	limit := 2

	var seen []string
	for _, offset := range []int{0, 2} {
		page := platform.restaurantPage(t,
			&customer.ListRestaurantsParams{Limit: &limit, Offset: &offset})
		if page.Page.Total != 3 {
			t.Errorf("offset=%d: total=%d, ожидали 3 — общее число не зависит от окна",
				offset, page.Page.Total)
		}
		seen = append(seen, names(page)...)
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

	if card.Id != id {
		t.Errorf("карточка отдала id=%q, ожидали %q", card.Id, id)
	}
	if card.Name != "Пекарня «Батон»" {
		t.Errorf("карточка отдала name=%q, ожидали «Пекарня «Батон»»", card.Name)
	}
}

func TestCardOfRestaurantThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).missingCard(t,
		uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a01"))

	if document.Code != customer.RestaurantNotFound {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.RestaurantNotFound)
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

	if code := platform.missingCard(t, id).Code; code != customer.RestaurantNotFound {
		t.Errorf("код отказа %q, ожидали %q", code, customer.RestaurantNotFound)
	}
}

// Идентификатор, который не uuid, клиентом не отправить — его тип в контракте uuid, —
// поэтому такой запрос идёт сырым: он проверяет разбор параметров до обработчика.
func TestCardRefusesIdentifierThatIsNotAnIdentifier(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		platformhttp.CustomerAPIPrefix+"/restaurants/пекарня", http.StatusBadRequest)

	if document.Code != string(customer.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
}

// Карточка и меню — разные ресурсы: меню это коллекция неограниченного размера со своей
// пагинацией, и открытие карточки не должно тянуть её целиком.
func TestMenuIsAResourceOfItsOwnAndNotAPartOfTheCard(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	id := platform.openRestaurant(t, "Пекарня «Батон»")
	card := platformhttp.CustomerAPIPrefix + "/restaurants/" + id.String()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(platform.get(t, card).Body, &fields); err != nil {
		t.Fatalf("разобрать карточку заведения: %v", err)
	}
	for _, field := range []string{"menu", "items", "products"} {
		if _, embedded := fields[field]; embedded {
			t.Errorf("в карточке заведения есть поле %q: меню в карточку не встраивается", field)
		}
	}

	menu := platform.get(t, card+"/products?limit=10&offset=0")
	if menu.Status == http.StatusNotFound {
		t.Error("меню заведения не отдаётся собственным адресом со своей пагинацией")
	}
}

// Тело запроса читается в память до того, как хоть одно правило успеет сказать, что
// запрос неверен, поэтому предел на размер стоит раньше разбора.
func TestRequestBodyLargerThanTheLimitIsRefusedWithoutBeingRead(t *testing.T) {
	t.Parallel()

	oversized := bytes.Repeat([]byte("a"), 2*httpx.MB)

	document := asRefusal(t, newPlatform(t).post(t,
		platformhttp.CustomerAPIPrefix+"/orders", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != string(customer.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
	if document.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("в теле отказа status=%d, ожидали 413", document.Status)
	}
}

// Тело без объявленной длины (chunked) отдаётся тем же отказом, что и объявленная длина
// сверх предела: причина у клиента одна, и выбирать по ней ветку поведения он должен
// одинаково, как бы тело ни было отправлено.
func TestRequestBodyLargerThanTheLimitIsRefusedTheSameWayWhenItsLengthIsNotDeclared(t *testing.T) {
	t.Parallel()

	// Строка JSON, которой нет конца: разбор тела читает её, пока чтение не оборвётся на
	// пределе. Мусор из одних букв оборвал бы разбор на первом же знаке, не дойдя до
	// предела, — и проверялся бы не он.
	oversized := append([]byte(`"`), bytes.Repeat([]byte("a"), httpx.MB+1024)...)

	document := asRefusal(t, newPlatform(t).postWithoutDeclaredLength(t,
		platformhttp.CustomerAPIPrefix+"/orders", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != string(customer.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
	if strings.Contains(document.Detail, "http: request body too large") {
		t.Errorf("в detail протекло сообщение стандартной библиотеки: %q", document.Detail)
	}
}

// Отказ по вине площадки отдаётся в том же формате с кодом `internal_error`, но без
// подробностей: причина уходит в лог, а не наружу.
//
// Площадка ломается по-настоящему — таблицей, которую читает витрина; репозиторий при этом
// не подменяется. Соседние тесты это не задевает: база у каждого теста своя (см.
// internal/pkg/testdb), и `DROP` виден только внутри неё.
//
// `CASCADE` нужен потому, что на заведения ссылаются соседние таблицы схемы: без него
// снос таблицы отклонила бы сама база, и тест проверял бы не то, ради чего написан.
func TestFailureOnThePlatformSideIsRefusedWithoutTellingTheClientWhy(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	if _, err := platform.db.ExecContext(t.Context(), `DROP TABLE platform.restaurants CASCADE`); err != nil {
		t.Fatalf("убрать таблицу заведений: %v", err)
	}

	document := platform.refusal(t,
		platformhttp.CustomerAPIPrefix+"/restaurants", http.StatusInternalServerError)

	if document.Code != string(customer.InternalError) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.InternalError)
	}
	if strings.Contains(document.Detail, "platform.restaurants") {
		t.Errorf("в detail попало устройство службы: %q", document.Detail)
	}
}
