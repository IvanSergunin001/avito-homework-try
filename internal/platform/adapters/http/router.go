// Package http — HTTP-адаптер площадки: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/platform` и через который ходят тесты шва площадки.
package http

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
)

// NewRouter собирает роутер площадки. Сейчас это общая обвязка и проба здоровья;
// поверхности API (`/api/v1`, `/api/partner/v1`, `/api/admin/v1`) монтируются сюда
// на следующих этапах.
func NewRouter(log *slog.Logger) *chi.Mux {
	return httpx.NewRouter(log)
}
