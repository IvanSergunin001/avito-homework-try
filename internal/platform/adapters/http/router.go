// Package http — HTTP-адаптер площадки: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/platform` и через который ходят тесты шва площадки.
package http

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
)

// NewRouter собирает роутер площадки: общая обвязка, проба здоровья и поверхности API.
// Клиентская поверхность (`/api/v1`) уже смонтирована; партнёрская и админская
// монтируются сюда на своих этапах — их серверные интерфейсы уже сгенерированы по
// спецификациям.
func NewRouter(log *slog.Logger) *chi.Mux {
	r := httpx.NewRouter(log)

	mountCustomerAPI(r)

	return r
}
