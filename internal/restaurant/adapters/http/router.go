// Package http — HTTP-адаптер коробки заведения: здесь собирается роутер со всеми middleware,
// который поднимает `cmd/restaurant` и через который ходят тесты шва коробки заведения.
package http

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/platform_client"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/postgres"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// Settings — то, с чем коробка поднимается.
//
// Всё это приходит параметром, а не читается здесь из окружения: коробка не должна вести
// себя по-разному в зависимости от того, что стоит в окружении процесса, — иначе тест шва
// проверял бы не её, а переменные окружения машины, на которой его запустили.
type Settings struct {
	// Hours — часы работы заведения: вне их коробка заказы не принимает.
	Hours domain.WorkingHours
	// Platform — куда и чем коробка стучится, уведомляя площадку.
	Platform PlatformLink
	// Retry — как коробка повторяет недоставленное уведомление.
	Retry domain.RetryPolicy
	// AutoAdvance — двигать ли статусы по таймеру. Это переключатель демонстрации: в
	// эксплуатации заказы двигает оператор, и по умолчанию он выключен.
	AutoAdvance bool
}

// PlatformLink — связь коробки с площадкой.
type PlatformLink struct {
	// BaseURL — адрес площадки, например `http://platform:8080`.
	BaseURL string
	// PartnerKey — партнёрский ключ, выданный заведению при онбординге: партнёрские
	// операции площадки закрыты им.
	PartnerKey string
	// Timeout — сколько коробка ждёт ответа площадки на уведомление.
	Timeout time.Duration
}

// Background — фоновые работы коробки: то, что служба ведёт рядом с обработкой запросов.
//
// Они отдаются наружу вместе с роутером, а не запускаются здесь: собирать их второй раз в
// `cmd` значило бы держать рядом с коробкой её неточную копию — и однажды обнаружить, что
// фоновая доставка уведомлений ходит в площадку не так, как та, что собрана в тестах.
// Решение, запускать ли их, принимает служба: у теста своя воля, когда двигать время.
type Background struct {
	// Notifications — доставка площадке уведомлений о смене статуса заказа.
	Notifications *application.Notifications
	// Autopilot — продвижение заказов по таймеру для демонстрации.
	Autopilot *application.Autopilot
}

// NewRouter собирает коробку целиком: хранилище поверх базы, сценарии над ним и роутер
// с общей обвязкой и её API. Сборка живёт здесь, а не в `cmd`, чтобы тест шва поднимал ту
// же самую коробку, что и запуск, а не её похожую копию.
//
// Документа два: партнёрский контракт (`/api/kitchen/v1/restaurant`), который коробка
// обязана реализовать наравне с заведением, у которого есть своя система, и рабочее место
// оператора (`/api/kitchen/v1/restaurant/operator`), которым заведение без своих систем
// ведёт меню, разбирает входящие заказы и двигает их по кухне. И меню, и заказы за ними
// одни и те же.
//
// Всё, с чем коробка поднимается, приходит параметром, а не читается здесь из окружения:
// роутер не должен вести себя по-разному в зависимости от того, что стоит в окружении
// процесса.
func NewRouter(log *slog.Logger, db *sql.DB, settings Settings) (*chi.Mux, Background) {
	incoming := postgres.NewOrders(db)
	menu := application.NewMenu(postgres.NewMenu(db))
	orders := application.NewOrders(incoming, settings.Hours)
	// Уведомления складывает в очередь то же самое хранилище заказов — той же
	// транзакцией, что и смену статуса. Здесь собирается вторая половина: доставка
	// накопившегося площадке.
	notifications := application.NewNotifications(
		postgres.NewNotifications(db),
		platform_client.NewStatusNotifier(
			settings.Platform.BaseURL, settings.Platform.PartnerKey, settings.Platform.Timeout),
		settings.Retry, log)

	r := httpx.NewRouter(log)

	mountPartnerAPI(r, menu, orders, log)
	mountOperatorAPI(r, menu, orders, log)

	return r, Background{
		Notifications: notifications,
		Autopilot:     application.NewAutopilot(incoming, settings.AutoAdvance, log),
	}
}
