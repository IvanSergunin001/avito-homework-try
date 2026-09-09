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
// С заказом путь обратный: тест передаёт его по партнёрскому контракту, как это делает
// площадка, а разбирает на рабочем месте оператора — заказ тоже один, и увидеть его
// глазами обеих сторон можно только пройдя обе двери.
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

const (
	// operatorMenu — рабочее место оператора: им заведение ведёт меню.
	operatorMenu = restauranthttp.OperatorAPIPrefix + "/menu"
	// contractMenu — партнёрский контракт: этой выдачей меню видит площадка.
	contractMenu = restauranthttp.PartnerAPIPrefix + "/menu"
	// contractOrders — партнёрский контракт: этой ручкой площадка передаёт заказ.
	contractOrders = restauranthttp.PartnerAPIPrefix + "/orders"
	// operatorOrders — рабочее место оператора: здесь он разбирает входящие заказы.
	operatorOrders = restauranthttp.OperatorAPIPrefix + "/orders"
)

// box — коробка заведения, поднятая целиком для одного теста.
type box struct {
	// srv — служба, слушающая на своём порту: тест ходит в неё по HTTP — и как рабочее
	// место оператора, и как площадка, забирающая меню.
	srv *httptest.Server
	// db — база коробки, своя у каждого теста и с накатанными миграциями.
	db *testdb.DB
	// platform — площадка по ту сторону контракта: HTTP-приёмник уведомлений о смене
	// статуса. Настоящая площадка сюда не поднимается — коробка обязана разговаривать с
	// ней по контракту, и проверяется именно это.
	platform *platformStub
	// background — фоновые работы коробки: доставка уведомлений и автопилот. Тест ведёт
	// их сам, называя момент, а не ждёт срабатывания таймера: ожидание тика проверяло бы
	// планировщик Go, а сдвиг времени вперёд — правило, по которому назначается повтор.
	background restauranthttp.Background
	// log — то, что коробка написала в лог. По нему видно то, что не видно в ответах:
	// например, что попытки доставки уведомления кончились.
	log *logSink
}

// retryBaseDelay — пауза перед первым повтором доставки у коробки, поднятой тестом.
// Каждая следующая вдвое длиннее; тест не ждёт этих пауз, а называет момент сам.
const retryBaseDelay = time.Second

// retryAttempts — сколько попыток доставки делает коробка, поднятая тестом.
const retryAttempts = 3

// boxOptions — чем коробка теста отличается от обычной. Нулевое значение — обычная:
// круглосуточная, со стоящим автопилотом.
type boxOptions struct {
	// hours — часы работы заведения.
	hours domain.WorkingHours
	// autoAdvance — двигать ли статусы по таймеру.
	autoAdvance bool
}

// newBox поднимает коробку по её шву. Готовить окружение перед прогоном не нужно: базу
// оснастка поднимает сама (см. internal/pkg/testdb).
//
// Заведение в ней работает круглосуточно: у теста, который проверяет не расписание, не
// должно быть шанса упасть оттого, что его запустили ночью.
func newBox(t *testing.T) *box {
	t.Helper()

	return startBox(t, boxOptions{})
}

// openBox поднимает коробку с заданными часами работы.
func openBox(t *testing.T, hours domain.WorkingHours) *box {
	t.Helper()

	return startBox(t, boxOptions{hours: hours})
}

// boxOnAutopilot поднимает коробку с включённым продвижением статусов по таймеру.
func boxOnAutopilot(t *testing.T) *box {
	t.Helper()

	return startBox(t, boxOptions{autoAdvance: true})
}

// startBox поднимает коробку вместе с площадкой-приёмником уведомлений.
func startBox(t *testing.T, options boxOptions) *box {
	t.Helper()

	db := testdb.New(t)
	platform := newPlatformStub(t)

	retry, err := domain.NewRetryPolicy(retryAttempts, retryBaseDelay)
	if err != nil {
		t.Fatalf("собрать правило повторов: %v", err)
	}

	// Логи службы читает тест, но middleware, которое их пишет, остаётся на месте: шов
	// проверяется вместе со всей обвязкой, а не в обход неё.
	sink := &logSink{}
	router, background := restauranthttp.NewRouter(
		logger.New(sink, slog.LevelInfo), db.DB, restauranthttp.Settings{
			Hours: options.hours,
			Platform: restauranthttp.PlatformLink{
				BaseURL:    platform.srv.URL,
				PartnerKey: partnerKey,
				Timeout:    waitForPlatform,
			},
			Retry:       retry,
			AutoAdvance: options.autoAdvance,
		})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &box{srv: srv, db: db, platform: platform, background: background, log: sink}
}

// logSink — лог коробки, прочитанный тестом.
//
// Пишут в него и обработчики запросов, и фоновая доставка — из разных горутин, поэтому
// запись под замком: иначе тест ловил бы гонку вместо правила, которое проверяет.
type logSink struct {
	mu      sync.Mutex
	written strings.Builder
}

// Write принимает очередную запись лога.
func (s *logSink) Write(record []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.written.Write(record)
}

// String отдаёт всё, что коробка написала в лог к этому моменту.
func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.written.String()
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

	return b.do(t, http.MethodGet, path, nil, nil)
}

// send отправляет тело коробке: так оператор заводит и правит позиции меню.
func (b *box) send(t *testing.T, method, path string, body any) response {
	t.Helper()

	return b.sendWithHeaders(t, method, path, body, nil)
}

// sendWithHeaders отправляет тело вместе с заголовками: так площадка передаёт заказ —
// с ключом идемпотентности и номером попытки, объявленными контрактом.
func (b *box) sendWithHeaders(t *testing.T, method, path string, body any, headers http.Header) response {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("собрать тело запроса %s %s: %v", method, path, err)
	}

	return b.do(t, method, path, encoded, headers)
}

// do выполняет запрос и читает ответ целиком.
func (b *box) do(t *testing.T, method, path string, body []byte, headers http.Header) response {
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
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
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
//
// Позиция и её актуальная цена — расширения партнёрского контракта: по ним площадка
// чинит каталог, поэтому тест читает их так же, как читала бы она.
type problemDocument struct {
	Type              string `json:"type"`
	Title             string `json:"title"`
	Status            int    `json:"status"`
	Detail            string `json:"detail"`
	Code              string `json:"code"`
	ProductExternalID string `json:"product_external_id"`
	CurrentPrice      *int64 `json:"current_price"`
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
