// Команда platform поднимает HTTP-службу площадки Авито.Кухня.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/postgres"
)

// envPrefix — префикс переменных окружения площадки. Источник настроек один — окружение
// процесса, — а разделение их по сервисам идёт по префиксу.
const envPrefix = "PLATFORM_"

// settings — конфигурация площадки: общие для обеих служб настройки плюс её собственные.
//
// Общая часть встраивается, а не копируется: порт, таймаут остановки, уровень логов и
// строка подключения одинаковы у площадки и у коробки, и расходиться им незачем.
type settings struct {
	config.Config `koanf:",squash"`

	// AdminToken — админский токен площадки: им опознаются администратор, подключающий
	// заведения, и саппорт. Значения по умолчанию у него нет и быть не может: токен,
	// зашитый в код, известен всем, кто видел репозиторий, — а этим токеном меняется
	// адрес заведения, то есть то, куда уходят чужие заказы.
	AdminToken string `koanf:"admin_token"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("platform stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := settings{Config: config.Default(8080)}
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}
	if cfg.AdminToken == "" {
		// Служба без токена не поднимается вовсе, а не поднимается с закрытой админской
		// поверхностью: молча выключенный онбординг обнаружился бы в момент, когда
		// заведение уже ждёт подключения.
		return errors.New(envPrefix + "ADMIN_TOKEN не задан: без него админские операции площадки не выполняются")
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
	defer closeQuietly(ctx, log, db)

	return httpx.Serve(ctx, log, cfg.Config, platformhttp.NewRouter(log, db, cfg.AdminToken))
}

// closeQuietly закрывает подключение к базе: служба к этому моменту уже остановлена, и
// ошибка закрытия ни на что не влияет — но узнать о ней из лога стоит.
func closeQuietly(ctx context.Context, log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.ErrorContext(ctx, "close database", slog.Any("error", err))
	}
}
