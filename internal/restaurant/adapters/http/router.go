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
)

// NewRouter собирает коробку целиком: хранилище поверх базы, сценарии над ним и роутер
// с общей обвязкой и поверхностями API. Сборка живёт здесь, а не в `cmd`, чтобы тест шва
// поднимал ту же самую коробку, что и запуск, а не её похожую копию.
//
// Поверхности две: партнёрский контракт (`/api/v1`), который коробка обязана реализовать
// наравне с заведением, у которого есть своя система, и рабочее место оператора
// (`/api/operator/v1`), которым заведение без своих систем ведёт меню. Меню за ними одно.
func NewRouter(log *slog.Logger, db *sql.DB) *chi.Mux {
	menu := application.NewMenu(postgres.NewMenu(db))

	r := httpx.NewRouter(log)

	mountPartnerAPI(r, menu, log)
	mountOperatorAPI(r, menu, log)

	return r
}
