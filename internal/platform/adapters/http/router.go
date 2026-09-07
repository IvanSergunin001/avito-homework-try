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
// adminToken — админский токен из конфигурации площадки: им опознаются администратор и
// саппорт. Он приходит параметром, а не читается здесь из окружения: роутер не должен
// вести себя по-разному в зависимости от того, что стоит в окружении процесса.
//
// Поверхностей три: клиентская — открытая, партнёрская и админская — закрытые токеном.
func NewRouter(log *slog.Logger, db *sql.DB, adminToken string) *chi.Mux {
	restaurants := postgres.NewRestaurants(db)
	showcase := application.NewShowcase(restaurants)
	catalog := application.NewCatalog(restaurants, postgres.NewProducts(db))
	onboarding := application.NewOnboarding(restaurants)
	access := guard{gate: application.NewGatekeeper(restaurants, adminToken), log: log}

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, catalog, log)
	mountPartnerAPI(r, access)
	mountAdminAPI(r, access, onboarding, log)

	return r
}
