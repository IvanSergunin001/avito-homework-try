// Package http — HTTP-адаптер площадки: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/platform` и через который ходят тесты шва площадки.
package http

import (
	"database/sql"
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/postgres"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
)

// NewRouter собирает площадку целиком: хранилище поверх базы, сценарии над ним и роутер
// с общей обвязкой и её API. Сборка живёт здесь, а не в `cmd`, чтобы тест шва поднимал ту
// же самую площадку, что и запуск, а не её похожую копию.
//
// Клиентское API (`/api/kitchen/v1/customer`) уже смонтировано; партнёрское и админское
// монтируются сюда на своих этапах — их серверные интерфейсы уже сгенерированы по
// спецификациям.
func NewRouter(log *slog.Logger, db *sql.DB) *chi.Mux {
	showcase := application.NewShowcase(postgres.NewRestaurants(db))

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, log)

	return r
}
