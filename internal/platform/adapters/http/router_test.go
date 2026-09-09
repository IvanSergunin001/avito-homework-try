package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// newPlatform поднимает площадку целиком: реальный роутер со всеми middleware.
func newPlatform(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(platformhttp.NewRouter(logger.New(io.Discard, slog.LevelInfo)))
	t.Cleanup(srv.Close)

	return srv
}

// response — ответ службы, прочитанный целиком. Тестам нужны код, тип содержимого и тело;
// соединением распоряжается помощник, поэтому в тестах о нём думать не приходится.
type response struct {
	Status      int
	ContentType string
	Body        []byte
}

// get выполняет запрос к службе и возвращает прочитанный ответ.
func get(t *testing.T, srv *httptest.Server, path string) response {
	t.Helper()

	resp, err := srv.Client().Get(srv.URL + path)
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

func TestRunningPlatformReportsItIsHealthy(t *testing.T) {
	resp := get(t, newPlatform(t), "/healthz")

	if resp.Status != http.StatusOK {
		t.Fatalf("GET /healthz вернул %d, ожидали 200", resp.Status)
	}

	var probe struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Body, &probe); err != nil {
		t.Fatalf("разобрать ответ пробы: %v", err)
	}
	if probe.Status != "ok" {
		t.Errorf("status = %q, ожидали \"ok\"", probe.Status)
	}
}

func TestHealthProbeLivesOutsideVersionedAPI(t *testing.T) {
	srv := newPlatform(t)

	if resp := get(t, srv, "/api/kitchen/v1/healthz"); resp.Status != http.StatusNotFound {
		t.Errorf("GET /api/kitchen/v1/healthz вернул %d, ожидали 404: проба не версионируется", resp.Status)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	if resp := get(t, newPlatform(t), "/no-such-path"); resp.Status != http.StatusNotFound {
		t.Errorf("GET /no-such-path вернул %d, ожидали 404", resp.Status)
	}
}
