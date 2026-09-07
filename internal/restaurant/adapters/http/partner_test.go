package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pageOfMenuItems — страница меню глазами площадки: разбираем её так же, как разбирала бы
// синхронизация каталога, — по схеме из партнёрского контракта, а не по типам коробки.
type pageOfMenuItems struct {
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

func menuPage(t *testing.T, srv *httptest.Server, path string) pageOfMenuItems {
	t.Helper()

	resp := get(t, srv, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s вернул %d, ожидали 200: %s", path, resp.Status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var page pageOfMenuItems
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("разобрать страницу меню: %v", err)
	}

	return page
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

func TestMenuPageUsesWindowDeclaredInContractWhenPlatformDoesNotAskForOne(t *testing.T) {
	page := menuPage(t, newRestaurantBox(t), "/api/v1/menu")

	if page.Page.Limit != 100 || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта 100 и 0",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestMenuPageEchoesTheWindowThePlatformAskedFor(t *testing.T) {
	page := menuPage(t, newRestaurantBox(t), "/api/v1/menu?limit=500&offset=1000")

	if page.Page.Limit != 500 || page.Page.Offset != 1000 {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные 500 и 1000",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestEmptyMenuIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	page := menuPage(t, newRestaurantBox(t), "/api/v1/menu")

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом меню %d позиций при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestMenuRefusesPageLargerThanContractAllows(t *testing.T) {
	document := refusal(t, newRestaurantBox(t), "/api/v1/menu?limit=501", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", document.Status)
	}
}
