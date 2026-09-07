// Тесты площадки идут через её шов — HTTP-границу. За границей ничего не подменяется:
// поднимается настоящий роутер со всеми middleware и настоящий PostgreSQL с накатанными
// миграциями. Репозитории не подменяются моками, и проверять, какие методы были вызваны,
// тесты не умеют: проверка говорит о том, что видно снаружи, — код ответа, тело и
// изменившееся состояние, которое видно следующим запросом.
//
// Имя теста называет правило предметной области, а не функцию под тестом: по списку
// упавших тестов должно быть видно, какое правило системы нарушено.
//
// Это первый шов проекта, и структура здесь — образец для всех тестов, которые появятся
// дальше: сторону заведения на своих этапах подменит HTTP-заглушка, отвечающая по
// партнёрскому контракту, — шов останется на границе протокола.
package http_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// adminToken — админский токен площадки, поднятой для теста. Настоящий берётся из
// конфигурации службы; в тестах он такой же произвольный, каким будет в эксплуатации, —
// важно не значение, а то, что площадка сверяется именно с ним.
const adminToken = "admin-token-под-тест"

// boxAddress — адрес коробки заведения, который тесты дают заведению при подключении.
// Никто по нему не ходит: синхронизация каталога и оформление заказа приезжают своими
// слайсами и подменят сторону заведения HTTP-заглушкой — здесь адрес важен только как
// реквизит, который площадка хранит и меняет.
const boxAddress = "http://box.test:8081"

// platform — площадка, поднятая целиком для одного теста.
type platform struct {
	// srv — служба, слушающая на своём порту: тест ходит в неё по HTTP, как ходил бы
	// покупательский фронтенд.
	srv *httptest.Server
	// db — база площадки, своя у каждого теста и с накатанными миграциями: тесты готовят
	// через неё то, что система пока не умеет заводить сама, и заглядывают в состояние,
	// которого не видно ни на одной ручке, — например, в хранимый хеш ключа. Подменять
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
	srv := httptest.NewServer(platformhttp.NewRouter(logger.New(io.Discard, slog.LevelInfo), db.DB, adminToken))
	t.Cleanup(srv.Close)

	return &platform{srv: srv, db: db}
}

// response — ответ службы, прочитанный целиком. Тестам нужны код, тип содержимого и тело;
// соединением распоряжается помощник, поэтому в тестах о нём думать не приходится.
type response struct {
	Status      int
	ContentType string
	Body        []byte
	// Challenge — заголовок `WWW-Authenticate`: им отказ `401` называет схему, которой
	// в него стучатся.
	Challenge string
}

// get выполняет запрос к площадке и возвращает прочитанный ответ.
func (p *platform) get(t *testing.T, path string) response {
	t.Helper()

	return p.do(t, http.MethodGet, path, "", nil)
}

// do выполняет запрос к площадке с токеном token (пустой — без заголовка авторизации) и
// возвращает прочитанный ответ. Через него ходят все помощники: закрытые поверхности
// отличаются от открытой одним заголовком, и заводить ради него второй способ ходить
// в площадку значило бы проверять открытую и закрытую поверхности разной оснасткой.
func (p *platform) do(t *testing.T, method, path, token string, body []byte) response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, p.srv.URL+path, payload)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := p.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s %s: %v", method, path, err)
	}

	return response{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        read,
		Challenge:   resp.Header.Get("WWW-Authenticate"),
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

// post отправляет тело на площадку и возвращает прочитанный ответ.
func (p *platform) post(t *testing.T, path string, body []byte) response {
	t.Helper()

	return p.do(t, http.MethodPost, path, "", body)
}

// registration — подключённое заведение вместе с тем, что площадка сообщила о нём один
// раз: партнёрским ключом.
type registration struct {
	// ID — идентификатор заведения на площадке.
	ID uuid.UUID
	// BaseURL — адрес интеграции, с которым заведение подключили.
	BaseURL string
	// Key — партнёрский ключ открытым текстом: больше площадка его не покажет.
	Key string
}

// register подключает заведение админской ручкой — так же, как это сделает администратор
// площадки. Прямой вставкой в базу заведения больше не заводятся: тест, готовящий
// состояние в обход системы, проверяет систему, которой нет.
func (p *platform) register(t *testing.T, name, baseURL string) registration {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"base_url":%q}`, name, baseURL)

	resp := p.do(t, http.MethodPost, "/api/admin/v1/restaurants", adminToken, []byte(body))
	if resp.Status != http.StatusCreated {
		t.Fatalf("регистрация заведения %q вернула %d, ожидали 201: %s", name, resp.Status, resp.Body)
	}

	var registered struct {
		ID      uuid.UUID `json:"id"`
		BaseURL string    `json:"base_url"`
		Key     string    `json:"partner_key"`
	}
	if err := json.Unmarshal(resp.Body, &registered); err != nil {
		t.Fatalf("разобрать ответ регистрации: %v", err)
	}

	return registration{ID: registered.ID, BaseURL: registered.BaseURL, Key: registered.Key}
}

// change заменяет реквизиты заведения админской ручкой и возвращает ответ как есть:
// тесты доступа проверяют по нему отказ, тесты онбординга — новые реквизиты.
func (p *platform) change(t *testing.T, id uuid.UUID, name, baseURL string, isActive bool, token string) response {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"base_url":%q,"is_active":%t}`, name, baseURL, isActive)

	return p.do(t, http.MethodPut, "/api/admin/v1/restaurants/"+id.String(), token, []byte(body))
}

// openRestaurant заводит на площадке работающее заведение и возвращает его идентификатор.
func (p *platform) openRestaurant(t *testing.T, name string) uuid.UUID {
	t.Helper()

	return p.register(t, name, boxAddress).ID
}

// closedRestaurant заводит заведение, которое больше не работает.
//
// Подключается оно так же, как всякое другое, и выключается правкой реквизитов: отдельной
// ручки «выключить» у площадки нет — отключение это та же замена реквизитов, и разводить
// их значило бы обещать администратору разницу, которой нет.
func (p *platform) closedRestaurant(t *testing.T, name string) uuid.UUID {
	t.Helper()

	registered := p.register(t, name, boxAddress)

	if resp := p.change(t, registered.ID, name, boxAddress, false, adminToken); resp.Status != http.StatusOK {
		t.Fatalf("отключение заведения %q вернуло %d, ожидали 200: %s", name, resp.Status, resp.Body)
	}

	return registered.ID
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
