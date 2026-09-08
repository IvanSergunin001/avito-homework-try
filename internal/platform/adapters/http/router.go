// Package http — HTTP-адаптер площадки: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/platform` и через который ходят тесты шва площадки.
package http

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/postgres"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/restaurant_client"
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
//
// syncTimeout — сколько площадка ждёт заведение, забирая у него меню.
//
// Вместе с роутером наружу отдаётся синхронизация каталога: ту же самую, которую дёргает
// партнёрская ручка внеочередного обновления, служба ведёт по расписанию. Собирать её
// второй раз в `cmd` значило бы держать рядом с площадкой её неточную копию — и однажды
// обнаружить, что фоновая синхронизация ходит в заведение не так, как внеочередная.
func NewRouter(
	log *slog.Logger, db *sql.DB, adminToken string, syncTimeout time.Duration,
) (*chi.Mux, *application.CatalogSync) {
	restaurants := postgres.NewRestaurants(db)
	showcase := application.NewShowcase(restaurants)
	catalog := application.NewCatalog(restaurants, postgres.NewProducts(db))
	onboarding := application.NewOnboarding(restaurants)
	catalogSync := application.NewCatalogSync(restaurants,
		restaurant_client.NewMenuSource(syncTimeout), postgres.NewCatalog(db), log)
	access := guard{gate: application.NewGatekeeper(restaurants, adminToken), log: log}

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, catalog, log)
	mountPartnerAPI(r, access, catalogSync, log)
	mountAdminAPI(r, access, onboarding, log)

	return r, catalogSync
}
