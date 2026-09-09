package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	partner "github.com/IvanSergunin001/Test157/api/partner/v1"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// platformSide — сгенерированный по партнёрскому контракту клиент, которым в коробку
// ходит площадка. Тест проверяет коробку тем же клиентом, что и синхронизация каталога:
// разойтись с контрактом такой тест не может.
func platformSide(t *testing.T, srv *httptest.Server) *partner.ClientWithResponses {
	t.Helper()

	client, err := partner.NewClientWithResponses(srv.URL+restauranthttp.PartnerAPIPrefix,
		partner.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("собрать клиент партнёрского контракта: %v", err)
	}

	return client
}

// menuPage запрашивает страницу меню и требует, чтобы ответ был успешным.
func menuPage(t *testing.T, srv *httptest.Server, params *partner.ListMenuParams) partner.MenuPage {
	t.Helper()

	resp, err := platformSide(t, srv).ListMenuWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить меню: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("меню вернуло %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.JSON200
}

func TestMenuPageUsesWindowDeclaredInContractWhenPlatformDoesNotAskForOne(t *testing.T) {
	page := menuPage(t, newRestaurantBox(t), &partner.ListMenuParams{})

	if page.Page.Limit != restauranthttp.MenuPageDefault || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта %d и 0",
			page.Page.Limit, page.Page.Offset, restauranthttp.MenuPageDefault)
	}
}

func TestMenuPageEchoesTheWindowThePlatformAskedFor(t *testing.T) {
	limit, offset := restauranthttp.MenuPageMax, 1000

	page := menuPage(t, newRestaurantBox(t), &partner.ListMenuParams{Limit: &limit, Offset: &offset})

	if page.Page.Limit != limit || page.Page.Offset != offset {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные %d и %d",
			page.Page.Limit, page.Page.Offset, limit, offset)
	}
}

func TestEmptyMenuIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	page := menuPage(t, newRestaurantBox(t), &partner.ListMenuParams{})

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом меню %d позиций при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestMenuRefusesPageLargerThanContractAllows(t *testing.T) {
	limit := restauranthttp.MenuPageMax + 1

	resp, err := platformSide(t, newRestaurantBox(t)).ListMenuWithResponse(context.Background(),
		&partner.ListMenuParams{Limit: &limit})
	if err != nil {
		t.Fatalf("запросить меню: %v", err)
	}
	if resp.ApplicationproblemJSON400 == nil {
		t.Fatalf("меню вернуло %d, ожидали 400: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if code := resp.ApplicationproblemJSON400.Code; code != partner.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", code, partner.ValidationFailed)
	}
	if status := resp.ApplicationproblemJSON400.Status; status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", status)
	}
}

// Нечисловой `limit` клиентом не отправить — его тип в контракте целое, — поэтому такой
// запрос идёт сырым: он проверяет разбор параметров, который живёт до обработчика.
func TestMenuRefusesPageSizeThatIsNotANumber(t *testing.T) {
	document := refusal(t, newRestaurantBox(t),
		restauranthttp.PartnerAPIPrefix+"/menu?limit=неполное", http.StatusBadRequest)

	if document.Code != string(partner.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, partner.ValidationFailed)
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

// refusal требует, чтобы коробка отказала в формате problem+json, и возвращает отказ.
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
