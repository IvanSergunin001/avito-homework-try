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
// с общей обвязкой и поверхностями API. Сборка живёт здесь, а не в `cmd`, чтобы тест шва
// поднимал ту же самую площадку, что и запуск, а не её похожую копию.
//
// Клиентская поверхность (`/api/v1`) уже смонтирована; партнёрская и админская
// монтируются сюда на своих этапах — их серверные интерфейсы уже сгенерированы по
// спецификациям.
func NewRouter(log *slog.Logger, db *sql.DB) *chi.Mux {
	restaurants := postgres.NewRestaurants(db)
	showcase := application.NewShowcase(restaurants)
	catalog := application.NewCatalog(restaurants, postgres.NewProducts(db))

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, catalog, log)

	return r
}
