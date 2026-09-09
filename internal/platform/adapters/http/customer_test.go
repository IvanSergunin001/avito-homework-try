package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	customer "github.com/IvanSergunin001/Test157/api/customer/v1"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// catalog — сгенерированный по спецификации клиент витрины. Тест смотрит на площадку
// теми же глазами, что и покупательский фронтенд: разбирает ответ типами контракта, а
// не своей копией схемы, — разойтись с контрактом такой тест не может.
func catalog(t *testing.T, srv *httptest.Server) *customer.ClientWithResponses {
	t.Helper()

	client, err := customer.NewClientWithResponses(srv.URL+platformhttp.CustomerAPIPrefix,
		customer.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("собрать клиент витрины: %v", err)
	}

	return client
}

// restaurantPage запрашивает страницу каталога и требует, чтобы ответ был успешным.
func restaurantPage(
	t *testing.T, srv *httptest.Server, params *customer.ListRestaurantsParams,
) customer.RestaurantPage {
	t.Helper()

	resp, err := catalog(t, srv).ListRestaurantsWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить каталог: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("каталог вернул %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.JSON200
}

func TestCatalogPageUsesWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	page := restaurantPage(t, newPlatform(t), &customer.ListRestaurantsParams{})

	if page.Page.Limit != platformhttp.CustomerPageDefault || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта %d и 0",
			page.Page.Limit, page.Page.Offset, platformhttp.CustomerPageDefault)
	}
}

func TestCatalogPageEchoesTheWindowTheClientAskedFor(t *testing.T) {
	limit, offset := 5, 10

	page := restaurantPage(t, newPlatform(t),
		&customer.ListRestaurantsParams{Limit: &limit, Offset: &offset})

	if page.Page.Limit != limit || page.Page.Offset != offset {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные %d и %d",
			page.Page.Limit, page.Page.Offset, limit, offset)
	}
}

func TestEmptyCatalogIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	page := restaurantPage(t, newPlatform(t), &customer.ListRestaurantsParams{})

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом каталоге %d записей при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestCatalogRefusesPageLargerThanContractAllows(t *testing.T) {
	limit := platformhttp.CustomerPageMax + 1

	resp, err := catalog(t, newPlatform(t)).ListRestaurantsWithResponse(context.Background(),
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
	document := refusal(t, newPlatform(t),
		platformhttp.CustomerAPIPrefix+"/restaurants?limit=неполный", http.StatusBadRequest)

	if document.Code != string(customer.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
}

// TestOperationDescribedByContractButNotYetImplementedAnswersNotImplemented закрепляет
// решение: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе.
func TestOperationDescribedByContractButNotYetImplementedAnswersNotImplemented(t *testing.T) {
	document := refusal(t, newPlatform(t),
		platformhttp.CustomerAPIPrefix+"/products", http.StatusNotImplemented)

	if document.Code != string(customer.NotImplemented) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.NotImplemented)
	}
}

// problemDocument — отказ в формате problem+json, разобранный без клиента: сырые запросы
// проверяют то, до чего сгенерированный клиент не дотягивается.
type problemDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

// refusal требует, чтобы служба отказала в формате problem+json, и возвращает отказ.
func refusal(t *testing.T, srv *httptest.Server, path string, status int) problemDocument {
	t.Helper()

	resp := get(t, srv, path)
	if resp.Status != status {
		t.Fatalf("GET %s вернул %d, ожидали %d: %s", path, resp.Status, status, resp.Body)
	}
	if resp.ContentType != "application/problem+json" {
		t.Errorf("тип содержимого %q, ожидали application/problem+json", resp.ContentType)
	}

	var document problemDocument
	if err := json.Unmarshal(resp.Body, &document); err != nil {
		t.Fatalf("разобрать отказ: %v", err)
	}

	return document
}
