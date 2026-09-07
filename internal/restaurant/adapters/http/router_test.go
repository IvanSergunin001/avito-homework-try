package http_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRunningRestaurantBoxReportsItIsHealthy(t *testing.T) {
	t.Parallel()

	resp := newBox(t).get(t, "/healthz")

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
	t.Parallel()

	if resp := newBox(t).get(t, "/api/kitchen/v1/healthz"); resp.Status != http.StatusNotFound {
		t.Errorf("GET /api/kitchen/v1/healthz вернул %d, ожидали 404: проба не версионируется", resp.Status)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	t.Parallel()

	if resp := newBox(t).get(t, "/no-such-path"); resp.Status != http.StatusNotFound {
		t.Errorf("GET /no-such-path вернул %d, ожидали 404", resp.Status)
	}
}
