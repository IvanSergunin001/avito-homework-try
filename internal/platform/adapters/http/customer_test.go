package http_test

import (
	"context"
	"net/http"
	"testing"

	customer "github.com/IvanSergunin001/Test157/api/customer/v1"
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
func TestOperationDescribedByContractButNotYetImplementedAnswersNotImplemented(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		platformhttp.CustomerAPIPrefix+"/products", http.StatusNotImplemented)

	if document.Code != string(customer.NotImplemented) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.NotImplemented)
	}
}
