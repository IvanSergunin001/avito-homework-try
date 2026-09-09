// Package httpx собирает HTTP-обвязку, одинаковую для площадки и коробки заведения:
// цепочку middleware, пробу здоровья и запуск сервера с корректной остановкой.
//
// Имя с суффиксом `x`, а не `http`, — чтобы не спорить со стандартной библиотекой:
// обвязке `net/http` нужен в каждом файле, и пакет-тёзка заставлял бы переименовывать
// импорт всюду, где нужны оба.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// HealthPath — путь пробы здоровья. Проба живёт в корне, а не под `/api/v1`: её дёргают
// Compose, оркестратор и балансировщик, которые о версиях API ничего не знают, и ломать
// её выпуском новой версии API нельзя.
const HealthPath = "/healthz"

// NewRouter создаёт роутер с общей обвязкой: идентификатор запроса, лог каждого запроса,
// восстановление после паники и проба здоровья. Службы монтируют поверх него свои
// поверхности API.
func NewRouter(log *slog.Logger) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// middleware.RealIP не подключаем: он доверяет заголовкам X-Forwarded-For и
	// X-Real-IP, которые клиент подделывает сам, а доверенного прокси перед службами нет.
	//
	// Логгер стоит снаружи Recoverer, чтобы паника попала в лог как ответ 500,
	// а не потерялась вместе с запросом.
	r.Use(RequestLogger(log))
	r.Use(middleware.Recoverer)

	r.Get(HealthPath, health)

	return r
}

// health отвечает, что процесс жив и способен обслуживать запросы.
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.ErrorContext(r.Context(), "write health response", slog.Any("error", err))
	}
}
