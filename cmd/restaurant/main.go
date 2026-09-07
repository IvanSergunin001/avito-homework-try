// Команда restaurant поднимает HTTP-службу коробки заведения.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/postgres"
)

// envPrefix — префикс переменных окружения коробки заведения. Источник настроек один —
// окружение процесса, — а разделение их по сервисам идёт по префиксу.
const envPrefix = "RESTAURANT_"

func main() {
	if err := run(); err != nil {
		slog.Error("restaurant stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Default(8081)
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel).With(slog.String("service", "restaurant"))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Схему накатывает отдельный шаг запуска, а служба только подключается к готовой базе.
	db, err := postgres.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer closeQuietly(ctx, log, db)

	return httpx.Serve(ctx, log, cfg, restauranthttp.NewRouter(log, db))
}

// closeQuietly закрывает подключение к базе: служба к этому моменту уже остановлена, и
// ошибка закрытия ни на что не влияет — но узнать о ней из лога стоит.
func closeQuietly(ctx context.Context, log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.ErrorContext(ctx, "close database", slog.Any("error", err))
	}
}
