// Команда platform поднимает HTTP-службу площадки Авито.Кухня.
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
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// envPrefix — префикс переменных окружения площадки. Источник настроек один — окружение
// процесса, — а разделение их по сервисам идёт по префиксу.
const envPrefix = "PLATFORM_"

func main() {
	if err := run(); err != nil {
		slog.Error("platform stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Default(8080)
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel).With(slog.String("service", "platform"))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Схему накатывает отдельный шаг запуска, а служба только подключается к готовой базе.
	db, err := postgres.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer postgres.CloseQuietly(ctx, log, db)

	return httpx.Serve(ctx, log, cfg, platformhttp.NewRouter(log, db))
}
