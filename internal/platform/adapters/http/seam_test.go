// Тесты площадки идут через её шов — HTTP-границу. За границей ничего не подменяется:
// поднимается настоящий роутер со всеми middleware и настоящий PostgreSQL с накатанными
// миграциями. Репозитории не подменяются моками, и проверять, какие методы были вызваны,
// тесты не умеют: проверка говорит о том, что видно снаружи, — код ответа, тело и
// изменившееся состояние, которое видно следующим запросом.
//
// Имя теста называет правило предметной области, а не функцию под тестом: по списку
// упавших тестов должно быть видно, какое правило системы нарушено.
//
// Это первый шов проекта, и структура здесь задаёт форму тестов площадки: сторону
// заведения на своих этапах подменит HTTP-заглушка, отвечающая по партнёрскому
// контракту, — шов останется на границе протокола. Тесты коробки заведения
// (`internal/restaurant/adapters/http`) написаны в прежней форме — свободными функциями,
// без общего типа — и переедут на эту, когда у коробки появится собственный шов.
package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// platform — площадка, поднятая целиком для одного теста.
type platform struct {
	// srv — служба, слушающая на своём порту: тест ходит в неё по HTTP, как ходил бы
	// покупательский фронтенд.
	srv *httptest.Server
	// db — база площадки, своя у каждого теста и с накатанными миграциями: тесты готовят
	// через неё исходное состояние и заглядывают в него после запроса. Подменять
	// хранилище нечем — оно настоящее с первого же теста, и переезжать на настоящий
	// PostgreSQL задним числом, когда появятся репозитории, шву не придётся.
	db *testdb.DB
}

// newPlatform поднимает площадку по её шву. Готовить окружение перед прогоном не нужно:
// базу оснастка поднимает сама (см. internal/pkg/testdb).
func newPlatform(t *testing.T) *platform {
	t.Helper()

	db := testdb.New(t)

	// Логи службы в тестах никому не нужны, но middleware, которое их пишет, остаётся
	// на месте: шов проверяется вместе со всей обвязкой, а не в обход неё.
	srv := httptest.NewServer(platformhttp.NewRouter(logger.New(io.Discard, slog.LevelInfo), db.DB))
	t.Cleanup(srv.Close)

	return &platform{srv: srv, db: db}
}

// response — ответ службы, прочитанный целиком. Тестам нужны код, тип содержимого и тело;
// соединением распоряжается помощник, поэтому в тестах о нём думать не приходится.
type response struct {
	Status      int
	ContentType string
	Body        []byte
}

// get выполняет запрос к площадке и возвращает прочитанный ответ.
func (p *platform) get(t *testing.T, path string) response {
	t.Helper()

	resp, err := p.srv.Client().Get(p.srv.URL + path)
	if err != nil {
		t.Fatalf("запрос %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s: %v", path, err)
	}

	return response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}
}

// problemDocument — отказ в формате problem+json.
type problemDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

// refusal требует, чтобы площадка отказала в формате problem+json, и возвращает отказ.
func (p *platform) refusal(t *testing.T, path string, status int) problemDocument {
	t.Helper()

	return asRefusal(t, p.get(t, path), status)
}

// asRefusal разбирает ответ как отказ и проверяет, что отказ отдан в обещанном контрактом
// виде: тот код ответа, тип содержимого problem+json и разбираемое тело.
func asRefusal(t *testing.T, resp response, status int) problemDocument {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("площадка ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
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

// gotBody требует, чтобы площадка ответила тем ответом, которого тест ждал, и возвращает
// его тело.
//
// Сгенерированный клиент разбирает каждый описанный контрактом ответ в своё поле, и
// проверка «пришёл именно этот ответ» — это проверка, что поле не пустое. Проверка
// одинакова у всех ручек, поэтому и живёт в одном месте: иначе каждая новая ручка
// приносила бы ещё две-три её копии. `what` называет запрошенное, `expected` — ожидавшийся
// ответ; вместе они говорят, что именно пошло не так, ещё до чтения тела.
func gotBody[T any](t *testing.T, what, expected string, body *T, resp *http.Response, raw []byte) T {
	t.Helper()

	if body == nil {
		t.Fatalf("%s: площадка ответила %d, ожидали %s: %s", what, resp.StatusCode, expected, raw)
	}

	return *body
}

// names — названия элементов страницы в том порядке, в каком их отдала площадка.
//
// Название достаётся функцией, а не полем: до поля через параметр типа Go добраться не
// даёт, даже когда поле есть у всех подставляемых типов.
func names[T any](items []T, name func(T) string) []string {
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, name(item))
	}

	return got
}

// post отправляет тело на площадку и возвращает прочитанный ответ.
func (p *platform) post(t *testing.T, path string, body []byte) response {
	t.Helper()

	return p.send(t, path, bytes.NewReader(body))
}

// postWithoutDeclaredLength отправляет тело источником, длину которого клиент заранее не
// знает: `Content-Length` в таком запросе нет вовсе, тело уходит chunked'ом, и предел на
// размер проверяется уже чтением. Обёртка прячет `*bytes.Reader` за голым io.Reader —
// иначе клиент вычислит длину сам и объявит её.
//
// Заголовок покупателя обязателен по контракту: без него запрос отказали бы на разборе
// параметров, не дойдя до чтения тела, — а проверяется здесь как раз чтение.
func (p *platform) postWithoutDeclaredLength(t *testing.T, path string, body []byte) response {
	t.Helper()

	return p.send(t, path, struct{ io.Reader }{bytes.NewReader(body)}, func(r *http.Request) {
		r.Header.Set("User-Id", uuid.New().String())
	})
}

// send отправляет тело на площадку и возвращает прочитанный ответ.
func (p *platform) send(t *testing.T, path string, body io.Reader, prepare ...func(*http.Request)) response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.srv.URL+path, body)
	if err != nil {
		t.Fatalf("собрать запрос POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	for _, apply := range prepare {
		apply(request)
	}

	resp, err := p.srv.Client().Do(request)
	if err != nil {
		t.Fatalf("запрос POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ POST %s: %v", path, err)
	}

	return response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: read}
}

// openRestaurant заводит на площадке работающее заведение и возвращает его идентификатор.
//
// Заведение появляется прямо в базе: админской ручки регистрации на этом этапе ещё нет,
// а подменять хранилище нечем — оно настоящее. Как только регистрация появится, исходное
// состояние будет готовиться ею.
func (p *platform) openRestaurant(t *testing.T, name string) uuid.UUID {
	t.Helper()

	return p.restaurant(t, name, true)
}

// closedRestaurant заводит заведение, которое больше не работает.
func (p *platform) closedRestaurant(t *testing.T, name string) uuid.UUID {
	t.Helper()

	return p.restaurant(t, name, false)
}

func (p *platform) restaurant(t *testing.T, name string, working bool) uuid.UUID {
	t.Helper()

	// uuid v7 — тот же вид идентификатора, что площадка выдаёт заведениям сама:
	// монотонный, чтобы вставки не разносило по индексу.
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("сгенерировать идентификатор заведения: %v", err)
	}

	_, err = p.db.ExecContext(t.Context(),
		`INSERT INTO platform.restaurants (id, name, is_active) VALUES ($1, $2, $3)`, id, name, working)
	if err != nil {
		t.Fatalf("завести заведение %q: %v", name, err)
	}

	return id
}

// menuItem — позиция меню, которую тест кладёт заведению. Нули полей — обычная позиция:
// без описания, бесплатная и в наличии, — поэтому тест называет только то, что проверяет.
type menuItem struct {
	// Name — название, по которому позицию видно в меню и находит поиск.
	Name string
	// Description — описание позиции; пустое означает, что заведение его не дало.
	Description string
	// Price — цена в копейках.
	Price int64
	// OutOfStock — позиция в стоп-листе заведения.
	OutOfStock bool
}

// addProduct кладёт позицию в меню заведения и возвращает её идентификатор.
//
// Позиция появляется прямо в базе: синхронизации каталога на этом этапе ещё нет, а
// подменять хранилище нечем — оно настоящее. Как только каталог начнёт приезжать от
// заведения, исходное состояние будет готовиться им.
func (p *platform) addProduct(t *testing.T, restaurant uuid.UUID, item menuItem) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("сгенерировать идентификатор товара: %v", err)
	}

	_, err = p.db.ExecContext(t.Context(), `
		INSERT INTO platform.products (id, restaurant_id, name, description, price, is_available)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, restaurant, item.Name, item.Description, item.Price, !item.OutOfStock)
	if err != nil {
		t.Fatalf("завести товар %q: %v", item.Name, err)
	}

	return id
}
