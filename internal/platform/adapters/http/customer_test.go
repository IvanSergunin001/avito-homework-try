package http_test

import (
	"encoding/json"
	"net/http"
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
