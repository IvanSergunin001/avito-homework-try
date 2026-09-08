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
	"time"

	"github.com/google/uuid"

	admin "github.com/IvanSergunin001/Test157/api/admin/v1"
	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// staffMember — сотрудник площадки, которым тесты представляются админскому API.
// Значение произвольное: заголовок `User-Id` не удостоверяет никого — площадка верит
// ему по условию задания, и админские ручки в MVP не закрыты ничем (см. README).
var staffMember = uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1aff")

// boxAddress — адрес коробки заведения, который тесты дают заведению, когда за меню к
// нему никто не пойдёт: по этому адресу не отвечает никто. Тесты синхронизации подключают
// заведение по адресу поднятой заглушки — см. `catalog_sync_test.go`.
const boxAddress = "http://box.test:8081"

// boxTimeout — сколько площадка, поднятая для теста, ждёт заведение: и когда забирает у
// него меню, и когда передаёт ему заказ. Настоящие пределы берутся из конфигурации службы
// и различаются; здесь они такие же по смыслу — предел ожидания стороннего заведения, —
// но короче, чтобы тест не ждал вместе с ней.
const boxTimeout = 5 * time.Second

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

	return newPlatformWaitingForBox(t, boxTimeout)
}

// newPlatformWaitingForBox поднимает площадку, которая ждёт ответа заведения не дольше
// waitForBox. Отдельный помощник нужен правилам про молчащее заведение — «оно не держит
// синхронизацию вечно» и «оно не держит вечно покупателя», — и проверять их общим для
// всех тестов пределом значило бы ждать вместе с площадкой пять секунд на каждом прогоне.
func newPlatformWaitingForBox(t *testing.T, waitForBox time.Duration) *platform {
	t.Helper()

	db := testdb.New(t)

	// Логи службы в тестах никому не нужны, но middleware, которое их пишет, остаётся
	// на месте: шов проверяется вместе со всей обвязкой, а не в обход неё.
	router, _ := platformhttp.NewRouter(logger.New(io.Discard, slog.LevelInfo), db.DB,
		platformhttp.BoxTimeouts{CatalogSync: waitForBox, OrderPlacement: waitForBox})

	srv := httptest.NewServer(router)
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

// do выполняет запрос к площадке с ключом key (пустой — без заголовка авторизации) и
// возвращает прочитанный ответ. Сырым запросом ходят только те тесты, куда не дотягивается
// сгенерированный клиент: партнёрское API отвечает кодами вне контракта, пока его операции
// не реализованы, а разбор параметров проверяется значениями, которых в контракте нет.
func (p *platform) do(t *testing.T, method, path, key string, body []byte) response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}

	return p.send(t, p.request(t, method, path, key, payload))
}

// request собирает запрос к площадке. Отдельно от `do` он нужен там, где важно, каким
// источником уходит тело: из `*bytes.Reader` клиент вычисляет длину и объявляет её сам, а
// из голого io.Reader — не может (см. postWithoutDeclaredLength). Заголовки сверх общих
// дописывает вызывающий: собранный запрос он получает в руки.
func (p *platform) request(t *testing.T, method, path, key string, body io.Reader) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, p.srv.URL+path, body)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	return req
}

// send выполняет собранный запрос и возвращает прочитанный ответ. Собирают запрос
// помощники — им и решать, какими заголовками поверхность отличает звонящего.
func (p *platform) send(t *testing.T, req *http.Request) response {
	t.Helper()

	resp, err := p.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s %s: %v", req.Method, req.URL.Path, err)
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

	return p.do(t, http.MethodPost, path, "", body)
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

	req := p.request(t, http.MethodPost, path, "", struct{ io.Reader }{bytes.NewReader(body)})
	req.Header.Set("User-Id", uuid.New().String())

	return p.send(t, req)
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

// onboarding — сгенерированный по спецификации клиент админского API. Оснастка смотрит
// на площадку теми же глазами, что и внутренний инструмент администратора: разбирает
// ответ типами контракта, а не своей копией схемы, — разойтись с контрактом такой тест
// не может.
func (p *platform) onboarding(t *testing.T) *admin.ClientWithResponses {
	t.Helper()

	client, err := admin.NewClientWithResponses(p.srv.URL+platformhttp.AdminAPIPrefix,
		admin.WithHTTPClient(p.srv.Client()))
	if err != nil {
		t.Fatalf("собрать клиент админского API: %v", err)
	}

	return client
}

// register подключает заведение админской ручкой — так же, как это сделает администратор
// площадки. Прямой вставкой в базу заведения больше не заводятся: тест, готовящий
// состояние в обход системы, проверяет систему, которой нет.
func (p *platform) register(t *testing.T, name, baseURL string) registration {
	t.Helper()

	resp, err := p.onboarding(t).RegisterRestaurantWithResponse(t.Context(),
		&admin.RegisterRestaurantParams{UserId: staffMember},
		admin.NewRestaurant{Name: name, BaseUrl: baseURL})
	if err != nil {
		t.Fatalf("подключить заведение %q: %v", name, err)
	}
	if resp.JSON201 == nil {
		t.Fatalf("регистрация заведения %q вернула %d, ожидали 201: %s",
			name, resp.HTTPResponse.StatusCode, resp.Body)
	}

	return registration{ID: resp.JSON201.Id, BaseURL: resp.JSON201.BaseUrl, Key: resp.JSON201.PartnerKey}
}

// change правит реквизиты заведения админской ручкой и возвращает ответ как есть: тесты
// онбординга разбирают по нему новые реквизиты, а тесты отказов — сам отказ.
//
// Правка частичная: в requisites заполняется только то, что тест меняет. Остальное
// площадка обязана оставить как было, и именно это проверяют тесты, зовущие помощник с
// одним заполненным полем.
func (p *platform) change(
	t *testing.T, id uuid.UUID, requisites admin.RestaurantUpdate,
) *admin.UpdateRestaurantResponse {
	t.Helper()

	resp, err := p.onboarding(t).UpdateRestaurantWithResponse(t.Context(), id,
		&admin.UpdateRestaurantParams{UserId: staffMember}, requisites)
	if err != nil {
		t.Fatalf("изменить реквизиты заведения %s: %v", id, err)
	}

	return resp
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

	off := false
	if resp := p.change(t, registered.ID, admin.RestaurantUpdate{IsActive: &off}); resp.JSON200 == nil {
		t.Fatalf("отключение заведения %q вернуло %d, ожидали 200: %s",
			name, resp.HTTPResponse.StatusCode, resp.Body)
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
// Позиция появляется прямо в базе, хотя каталог уже умеет приезжать синхронизацией: это
// тесты витрины, и готовить их состояние выгрузкой значило бы проверять правила витрины
// через правила синхронизации — упавший тест поиска перестал бы отвечать на вопрос, что
// именно сломано. Тем же способом готовится состояние, которого выгрузка дать не может:
// меню заведения, которое уже не работает.
//
// Синхронизация проверяется там, где она и есть предмет проверки, — в `catalog_sync_test.go`,
// целиком через партнёрскую ручку и заглушку заведения.
func (p *platform) addProduct(t *testing.T, restaurant uuid.UUID, item menuItem) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("сгенерировать идентификатор товара: %v", err)
	}

	_, err = p.db.ExecContext(t.Context(), `
		INSERT INTO platform.products (id, restaurant_id, external_id, name, description, price, is_available)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, restaurant, id.String(), item.Name, item.Description, item.Price, !item.OutOfStock)
	if err != nil {
		t.Fatalf("завести товар %q: %v", item.Name, err)
	}

	return id
}
