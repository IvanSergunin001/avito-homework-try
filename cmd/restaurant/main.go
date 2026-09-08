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
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// envPrefix — префикс переменных окружения коробки заведения: обе службы живут в одном окружении
// Compose, и разделяет их только он.
const envPrefix = "RESTAURANT_"

func main() {
	if err := run(); err != nil {
		slog.Error("restaurant stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := config.DefaultService(8081)
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel).With(slog.String("service", "restaurant"))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return httpx.Serve(ctx, log, cfg, restauranthttp.NewRouter(log))
}
