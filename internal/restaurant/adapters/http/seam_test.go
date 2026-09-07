// Тесты коробки идут через её шов — HTTP-границу. За границей ничего не подменяется:
// поднимается настоящий роутер со всеми middleware и настоящий PostgreSQL с накатанными
// миграциями. Хранилище не подменяется моками, и проверять, какие методы были вызваны,
// тесты не умеют: проверка говорит о том, что видно снаружи, — код ответа, тело и
// изменившееся состояние, которое видно следующим запросом.
//
// Шов один на оба API коробки, и это важно: оператор правит меню своим, а результат тест
// читает на партнёрском — там, где его увидит площадка.
// Меню у заведения одно, и тест ходит теми же дверями, что настоящие потребители.
//
// Имя теста называет правило предметной области, а не функцию под тестом: по списку
// упавших тестов должно быть видно, какое правило системы нарушено.
package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

const (
	// operatorMenu — рабочее место оператора: им заведение ведёт меню.
	operatorMenu = restauranthttp.OperatorAPIPrefix + "/menu"
	// contractMenu — партнёрский контракт: этой выдачей меню видит площадка.
	contractMenu = restauranthttp.PartnerAPIPrefix + "/menu"
)

// box — коробка заведения, поднятая целиком для одного теста.
type box struct {
	// srv — служба, слушающая на своём порту: тест ходит в неё по HTTP — и как рабочее
	// место оператора, и как площадка, забирающая меню.
	srv *httptest.Server
	// db — база коробки, своя у каждого теста и с накатанными миграциями.
	db *testdb.DB
}

// newBox поднимает коробку по её шву. Готовить окружение перед прогоном не нужно: базу
// оснастка поднимает сама (см. internal/pkg/testdb).
func newBox(t *testing.T) *box {
	t.Helper()

	db := testdb.New(t)

	// Логи службы в тестах никому не нужны, но middleware, которое их пишет, остаётся
	// на месте: шов проверяется вместе со всей обвязкой, а не в обход неё.
	srv := httptest.NewServer(restauranthttp.NewRouter(logger.New(io.Discard, slog.LevelInfo), db.DB))
	t.Cleanup(srv.Close)

	return &box{srv: srv, db: db}
}

// response — ответ службы, прочитанный целиком. Тестам нужны код, тип содержимого и тело;
// соединением распоряжается помощник, поэтому в тестах о нём думать не приходится.
type response struct {
	Status      int
	ContentType string
	Body        []byte
}

// get выполняет запрос к коробке и возвращает прочитанный ответ.
func (b *box) get(t *testing.T, path string) response {
	t.Helper()

	return b.do(t, http.MethodGet, path, nil)
}

// send отправляет тело коробке: так оператор заводит и правит позиции меню.
func (b *box) send(t *testing.T, method, path string, body any) response {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("собрать тело запроса %s %s: %v", method, path, err)
	}

	return b.do(t, method, path, encoded)
}

// do выполняет запрос и читает ответ целиком.
func (b *box) do(t *testing.T, method, path string, body []byte) response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, b.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s %s: %v", method, path, err)
	}

	return response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: read}
}

// decode разбирает успешный ответ, ожидая обещанный код и тип содержимого.
func decode(t *testing.T, resp response, status int, target any) {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("коробка ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}
	if err := json.Unmarshal(resp.Body, target); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}
}

// problemDocument — отказ в формате problem+json.
type problemDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

// asRefusal разбирает ответ как отказ и проверяет, что отказ отдан в обещанном виде: тот
// код ответа, тип содержимого problem+json и разбираемое тело.
func asRefusal(t *testing.T, resp response, status int) problemDocument {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("коробка ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
	}
	if resp.ContentType != "application/problem+json" {
		t.Errorf("тип содержимого %q, ожидали application/problem+json", resp.ContentType)
	}

	var document problemDocument
	if err := json.Unmarshal(resp.Body, &document); err != nil {
		t.Fatalf("разобрать отказ: %v", err)
	}
	if document.Type == "" {
		t.Error("в отказе нет поля type, а problem+json без него — не problem+json")
	}

	return document
}

// operatorItem — позиция меню глазами оператора: с остатком, отметкой стоп-листа и
// посчитанной коробкой доступностью.
type operatorItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Price       int64   `json:"price"`
	Stock       int     `json:"stock"`
	InStopList  bool    `json:"in_stop_list"`
	IsAvailable bool    `json:"is_available"`
}

// operatorPage — страница меню на рабочем месте оператора.
type operatorPage struct {
	Items []operatorItem `json:"items"`
	Page  pageMeta       `json:"page"`
}

// pageMeta — окно выдачи, каким его вернули вместе со страницей.
type pageMeta struct {
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// newItem — позиция, которую оператор заводит в меню. Нули полей — бесплатная позиция
// без описания и без остатка, поэтому тест называет только то, что проверяет.
type newItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Price       int64  `json:"price"`
	Stock       int    `json:"stock"`
}

// addItem заводит позицию меню и возвращает её такой, какой её вернула коробка.
func (b *box) addItem(t *testing.T, item newItem) operatorItem {
	t.Helper()

	var added operatorItem
	decode(t, b.send(t, http.MethodPost, operatorMenu, item), http.StatusCreated, &added)

	return added
}

// markStopList отмечает позицию как отсутствующую или снимает отметку.
func (b *box) markStopList(t *testing.T, id string, inStopList bool) operatorItem {
	t.Helper()

	var marked operatorItem
	decode(t, b.send(t, http.MethodPut, operatorMenu+"/"+id+"/stop-list",
		map[string]bool{"in_stop_list": inStopList}), http.StatusOK, &marked)

	return marked
}

// setStock записывает остаток позиции.
func (b *box) setStock(t *testing.T, id string, stock int) operatorItem {
	t.Helper()

	var updated operatorItem
	decode(t, b.send(t, http.MethodPut, operatorMenu+"/"+id+"/stock",
		map[string]int{"stock": stock}), http.StatusOK, &updated)

	return updated
}

// operatorMenuPage читает страницу меню на рабочем месте оператора.
func (b *box) operatorMenuPage(t *testing.T, query string) operatorPage {
	t.Helper()

	var page operatorPage
	decode(t, b.get(t, operatorMenu+query), http.StatusOK, &page)

	return page
}
