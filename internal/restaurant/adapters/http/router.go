// Package http — HTTP-адаптер коробки заведения: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/restaurant` и через который ходят тесты шва коробки заведения.
package http

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
)

// NewRouter собирает роутер коробки заведения. Сейчас это общая обвязка и проба здоровья;
// поверхности API (партнёрский контракт и рабочее место оператора) монтируются сюда
// на следующих этапах.
func NewRouter(log *slog.Logger) *chi.Mux {
	return httpx.NewRouter(log)
}
