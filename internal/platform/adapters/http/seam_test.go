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
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

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
	srv := httptest.NewServer(platformhttp.NewRouter(logger.New(io.Discard, slog.LevelInfo)))
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

	resp := p.get(t, path)
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
