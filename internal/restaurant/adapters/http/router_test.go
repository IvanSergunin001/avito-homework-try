package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// newRestaurantBox поднимает коробку заведения целиком: реальный роутер со всеми middleware.
func newRestaurantBox(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(restauranthttp.NewRouter(logger.New(io.Discard, slog.LevelInfo)))
	t.Cleanup(srv.Close)

	return srv
}

// get выполняет запрос к службе и возвращает код ответа с телом.
func get(t *testing.T, srv *httptest.Server, path string) (int, []byte) {
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

	return resp.StatusCode, body
}

func TestRunningRestaurantBoxReportsItIsHealthy(t *testing.T) {
	status, body := get(t, newRestaurantBox(t), "/healthz")

	if status != http.StatusOK {
		t.Fatalf("GET /healthz вернул %d, ожидали 200", status)
	}

	var probe struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("разобрать ответ пробы: %v", err)
	}
	if probe.Status != "ok" {
		t.Errorf("status = %q, ожидали \"ok\"", probe.Status)
	}
}

func TestHealthProbeLivesOutsideVersionedAPI(t *testing.T) {
	srv := newRestaurantBox(t)

	if status, _ := get(t, srv, "/api/v1/healthz"); status != http.StatusNotFound {
		t.Errorf("GET /api/v1/healthz вернул %d, ожидали 404: проба не версионируется", status)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	if status, _ := get(t, newRestaurantBox(t), "/no-such-path"); status != http.StatusNotFound {
		t.Errorf("GET /no-such-path вернул %d, ожидали 404", status)
	}
}
