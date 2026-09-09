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
// API три: клиентское и админское открыты, партнёрское закрыто выданным заведению
// ключом. Токена у площадки нет ни одного — админские ручки закрывает не она, а контур,
// в котором её запускают; цена решения названа в спецификации и в README.
func NewRouter(log *slog.Logger, db *sql.DB) *chi.Mux {
	restaurants := postgres.NewRestaurants(db)
	showcase := application.NewShowcase(restaurants)
	catalog := application.NewCatalog(restaurants, postgres.NewProducts(db))
	onboarding := application.NewOnboarding(restaurants)
	access := guard{gate: application.NewGatekeeper(restaurants), log: log}

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, catalog, log)
	mountPartnerAPI(r, access, log)
	mountAdminAPI(r, onboarding, log)

	return r
}
