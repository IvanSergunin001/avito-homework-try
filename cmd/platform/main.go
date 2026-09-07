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
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// envPrefix — префикс переменных окружения площадки: обе службы живут в одном окружении
// Compose, и разделяет их только он.
const envPrefix = "PLATFORM_"

func main() {
	if err := run(); err != nil {
		slog.Error("platform stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := config.DefaultService(8080)
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel).With(slog.String("service", "platform"))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return httpx.Serve(ctx, log, cfg.Addr(), platformhttp.NewRouter(log), cfg.ShutdownTimeout)
}
