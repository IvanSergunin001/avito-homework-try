// Команда restaurant поднимает HTTP-службу коробки заведения.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/postgres"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// envPrefix — префикс переменных окружения коробки заведения. Источник настроек один —
// окружение процесса, — а разделение их по сервисам идёт по префиксу.
const envPrefix = "RESTAURANT_"

// settings — конфигурация коробки: общие для обеих служб настройки плюс её собственные.
//
// Общая часть встраивается, а не копируется: порт, таймаут остановки, уровень логов и
// строка подключения одинаковы у площадки и у коробки, и расходиться им незачем.
type settings struct {
	config.Config `koanf:",squash"`

	// OpenFrom и OpenUntil — часы работы заведения, «ЧЧ:ММ» по местному времени коробки.
	// Вне их коробка отказывает площадке кодом `restaurant_closed`: принять заказ, который
	// некому готовить, хуже, чем отказать сразу.
	//
	// Умолчание — круглосуточная работа (одинаковые значения): коробка, которой не сказали
	// часов, принимает заказы всегда, а не отказывает во всех подряд. Расписание живёт
	// настройкой, а не переключателем в базе: забытый на ночь переключатель означал бы
	// заказы, которые некому выполнять.
	OpenFrom  string `koanf:"open_from"`
	OpenUntil string `koanf:"open_until"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("restaurant stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := settings{
		Config:    config.Default(8081),
		OpenFrom:  "00:00",
		OpenUntil: "00:00",
	}
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}

	hours, err := domain.NewWorkingHours(cfg.OpenFrom, cfg.OpenUntil)
	if err != nil {
		// Служба с нечитаемым расписанием не поднимается вовсе: расписание, которое
		// коробка не поняла, — это либо круглосуточный приём заказов вместо дневного,
		// либо отказ всем подряд, и узнать об этом от покупателя дороже, чем не стартовать.
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel).With(slog.String("service", "restaurant"))
	slog.SetDefault(log)
	log.Info("restaurant working hours", slog.String("hours", hours.String()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Схему накатывает отдельный шаг запуска, а служба только подключается к готовой базе.
	db, err := postgres.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer postgres.CloseQuietly(ctx, log, db)

	return httpx.Serve(ctx, log, cfg.Config, restauranthttp.NewRouter(log, db, hours))
}
