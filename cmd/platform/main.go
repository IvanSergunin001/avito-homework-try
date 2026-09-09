// Команда platform поднимает HTTP-службу площадки Авито.Кухня.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	"github.com/IvanSergunin001/Test157/internal/pkg/postgres"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
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

	router, catalog := platformhttp.NewRouter(log, db, cfg.CatalogSyncTimeout)

	// Каталог площадка забирает сама: заведение не обязано уметь доставлять изменения, а
	// площадка обязана показывать актуальное меню. Обход идёт рядом со службой и
	// заканчивается вместе с ней — контекст у них общий.
	go catalog.RunPeriodically(ctx, cfg.CatalogSyncInterval)

	return httpx.Serve(ctx, log, cfg.Config, router)
}
