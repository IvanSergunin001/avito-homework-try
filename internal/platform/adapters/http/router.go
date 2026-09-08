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
// timeouts — сколько площадка ждёт заведение: отдельно на выгрузку меню и отдельно на
// переданный заказ.
//
// Вместе с роутером наружу отдаётся синхронизация каталога: ту же самую, которую дёргает
// партнёрская ручка внеочередного обновления, служба ведёт по расписанию. Собирать её
// второй раз в `cmd` значило бы держать рядом с площадкой её неточную копию — и однажды
// обнаружить, что фоновая синхронизация ходит в заведение не так, как внеочередная.
func NewRouter(
	log *slog.Logger, db *sql.DB, adminToken string, timeouts BoxTimeouts,
) (*chi.Mux, *application.CatalogSync) {
	restaurants := postgres.NewRestaurants(db)
	products := postgres.NewProducts(db)
	showcase := application.NewShowcase(restaurants)
	catalog := application.NewCatalog(restaurants, products)
	onboarding := application.NewOnboarding(restaurants)
	catalogSync := application.NewCatalogSync(restaurants,
		restaurant_client.NewMenuSource(timeouts.CatalogSync), postgres.NewCatalog(db), log)
	// Синхронизация каталога достаётся оформлению той же самой: заведение, ответившее
	// «цена не та», отправляет площадку за меню вне очереди — тем же сценарием, которым
	// она ходит за ним по расписанию и по просьбе заведения.
	orders := postgres.NewOrders(db)
	ordering := application.NewOrdering(restaurants, products, orders,
		restaurant_client.NewOrderIntake(timeouts.OrderPlacement), catalogSync, log)
	// Продвижение заказа — самостоятельный сценарий над тем же хранилищем: заказ двигают
	// уведомления заведения, а не оформление, которое к этому моменту давно закончилось.
	progress := application.NewProgress(orders, log)
	access := guard{gate: application.NewGatekeeper(restaurants, adminToken), log: log}

	r := httpx.NewRouter(log)

	mountCustomerAPI(r, showcase, catalog, ordering, log)
	mountPartnerAPI(r, access, catalogSync, progress, log)
	mountAdminAPI(r, access, onboarding, log)

	return r, catalogSync
}

// BoxTimeouts — сколько площадка ждёт коробку заведения.
//
// Пределы разные, потому что ждут разного. Выгрузку меню никто не ждёт перед экраном:
// синхронизация идёт в фоне, и заведению можно дать время. Заказ ждёт человек, и предел
// ожидания здесь — это то, сколько он смотрит на крутящийся индикатор, прежде чем
// получить внятный ответ.
type BoxTimeouts struct {
	// CatalogSync — сколько ждём страницу меню, забирая каталог заведения.
	CatalogSync time.Duration
	// OrderPlacement — сколько ждём решения заведения по переданному заказу.
	OrderPlacement time.Duration
}
