// Package http — HTTP-адаптер коробки заведения: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/restaurant` и через который ходят тесты шва коробки заведения.
package http

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
)

// NewRouter собирает роутер коробки заведения: общая обвязка, проба здоровья и
// партнёрский контракт (`/api/kitchen/v1/restaurant`), который коробка обязана реализовать наравне с
// заведением, у которого есть своя система. Рабочее место оператора монтируется сюда
// на своём этапе.
func NewRouter(log *slog.Logger) *chi.Mux {
	r := httpx.NewRouter(log)

	mountPartnerAPI(r)

	return r
}
