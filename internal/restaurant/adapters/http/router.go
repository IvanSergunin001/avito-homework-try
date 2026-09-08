// Package http — HTTP-адаптер коробки заведения: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/restaurant` и через который ходят тесты шва коробки заведения.
package http

import (
	"database/sql"
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/postgres"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// NewRouter собирает коробку целиком: хранилище поверх базы, сценарии над ним и роутер
// с общей обвязкой и поверхностями API. Сборка живёт здесь, а не в `cmd`, чтобы тест шва
// поднимал ту же самую коробку, что и запуск, а не её похожую копию.
//
// Поверхности две: партнёрский контракт (`/api/v1`), который коробка обязана реализовать
// наравне с заведением, у которого есть своя система, и рабочее место оператора
// (`/api/operator/v1`), которым заведение без своих систем ведёт меню и разбирает
// входящие заказы. И меню, и заказы за ними одни и те же.
//
// hours — часы работы заведения из конфигурации коробки: вне их коробка заказы не
// принимает. Они приходят параметром, а не читаются здесь из окружения: роутер не должен
// вести себя по-разному в зависимости от того, что стоит в окружении процесса.
func NewRouter(log *slog.Logger, db *sql.DB, hours domain.WorkingHours) *chi.Mux {
	menu := application.NewMenu(postgres.NewMenu(db))
	orders := application.NewOrders(postgres.NewOrders(db), hours)

	r := httpx.NewRouter(log)

	mountPartnerAPI(r, menu, orders, log)
	mountOperatorAPI(r, menu, orders, log)

	return r
}
