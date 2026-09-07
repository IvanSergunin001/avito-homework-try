package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pageOfRestaurants — ответ каталога глазами клиента: разбираем его так же, как разбирал бы
// покупательский фронтенд, — по схеме из спецификации, а не по внутренним типам площадки.
type pageOfRestaurants struct {
	Items []json.RawMessage `json:"items"`
	Page  struct {
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
		Total  int64 `json:"total"`
	} `json:"page"`
}

// problemDocument — отказ в формате problem+json.
type problemDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func catalogPage(t *testing.T, srv *httptest.Server, path string) pageOfRestaurants {
	t.Helper()

	resp := get(t, srv, path)
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

func TestCatalogPageUsesWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	page := catalogPage(t, newPlatform(t), "/api/v1/restaurants")

	if page.Page.Limit != 20 || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта 20 и 0",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestCatalogPageEchoesTheWindowTheClientAskedFor(t *testing.T) {
	page := catalogPage(t, newPlatform(t), "/api/v1/restaurants?limit=5&offset=10")

	if page.Page.Limit != 5 || page.Page.Offset != 10 {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные 5 и 10",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestEmptyCatalogIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	page := catalogPage(t, newPlatform(t), "/api/v1/restaurants")

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом каталоге %d записей при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestCatalogRefusesPageLargerThanContractAllows(t *testing.T) {
	document := refusal(t, newPlatform(t), "/api/v1/restaurants?limit=101", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", document.Status)
	}
}

func TestCatalogRefusesPageSizeThatIsNotANumber(t *testing.T) {
	document := refusal(t, newPlatform(t), "/api/v1/restaurants?limit=неполный", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}
