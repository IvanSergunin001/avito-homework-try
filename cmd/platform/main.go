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
	"time"

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

	// CatalogSyncInterval — как часто площадка сама забирает меню у каждого работающего
	// заведения. Интервал живёт конфигурацией, а не константой: он балансирует свежесть
	// витрины против нагрузки на чужие системы, и подбирается он в эксплуатации, а не
	// при написании кода. Заведение, которому ждать очередного цикла долго, зовёт
	// внеочередное обновление.
	CatalogSyncInterval time.Duration `koanf:"catalog_sync_interval"`

	// CatalogSyncTimeout — сколько площадка ждёт заведение, забирая у него страницу меню.
	// Без таймаута заведение, принявшее соединение и замолчавшее, останавливало бы
	// синхронизацию всех остальных.
	CatalogSyncTimeout time.Duration `koanf:"catalog_sync_timeout"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("platform stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := settings{
		Config:              config.Default(8080),
		CatalogSyncInterval: 30 * time.Second,
		CatalogSyncTimeout:  15 * time.Second,
	}
	if err := config.Load(envPrefix, &cfg); err != nil {
		return err
	}
	if cfg.CatalogSyncInterval <= 0 {
		// Нулевой интервал — это не «синхронизировать как можно чаще», а «загрузить собой
		// каждое заведение»: служба с такой конфигурацией не поднимается вовсе.
		return errors.New(envPrefix + "CATALOG_SYNC_INTERVAL должен быть больше нуля")
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

	router, catalog := platformhttp.NewRouter(log, db, cfg.AdminToken, cfg.CatalogSyncTimeout)

	// Каталог площадка забирает сама: заведение не обязано уметь доставлять изменения, а
	// площадка обязана показывать актуальное меню. Обход идёт рядом со службой и
	// заканчивается вместе с ней — контекст у них общий.
	go catalog.RunPeriodically(ctx, cfg.CatalogSyncInterval)

	return httpx.Serve(ctx, log, cfg.Config, router)
}

// closeQuietly закрывает подключение к базе: служба к этому моменту уже остановлена, и
// ошибка закрытия ни на что не влияет — но узнать о ней из лога стоит.
func closeQuietly(ctx context.Context, log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.ErrorContext(ctx, "close database", slog.Any("error", err))
	}
}
