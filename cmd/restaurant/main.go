// Команда restaurant поднимает HTTP-службу коробки заведения.
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
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/postgres"
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

	// PlatformBaseURL и PartnerKey — адрес площадки и выданный заведению при онбординге
	// партнёрский ключ: ими коробка уведомляет площадку о смене статуса заказа.
	// Умолчаний у них нет — площадка у каждого заведения своя, а ключ, зашитый в код,
	// известен всем, кто видел репозиторий.
	PlatformBaseURL string `koanf:"platform_base_url"`
	PartnerKey      string `koanf:"partner_key"`

	// NotifyInterval — как часто коробка развозит накопившиеся уведомления. Он же —
	// задержка между сменой статуса и тем, как о ней узнает покупатель, поэтому короткий.
	NotifyInterval time.Duration `koanf:"notify_interval"`
	// NotifyTimeout — сколько коробка ждёт ответа площадки на уведомление.
	NotifyTimeout time.Duration `koanf:"notify_timeout"`
	// NotifyMaxAttempts и NotifyRetryDelay — сколько попыток доставки коробка делает и
	// какова пауза перед первым повтором; каждая следующая вдвое длиннее.
	NotifyMaxAttempts int           `koanf:"notify_max_attempts"`
	NotifyRetryDelay  time.Duration `koanf:"notify_retry_delay"`

	// AutoAdvanceOrders двигает статусы заказов по таймеру — для демонстрации пути
	// покупателя без переключения на рабочее место оператора. По умолчанию выключен: в
	// эксплуатации статусы двигает оператор, он единственный знает, что на кухне.
	AutoAdvanceOrders bool `koanf:"auto_advance_orders"`
	// AutoAdvanceInterval — сколько заказ проводит на каждой ступени при включённом
	// автопилоте.
	AutoAdvanceInterval time.Duration `koanf:"auto_advance_interval"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("restaurant stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg := settings{
		Config:              config.Default(8081),
		OpenFrom:            "00:00",
		OpenUntil:           "00:00",
		NotifyInterval:      time.Second,
		NotifyTimeout:       5 * time.Second,
		NotifyMaxAttempts:   3,
		NotifyRetryDelay:    2 * time.Second,
		AutoAdvanceInterval: 15 * time.Second,
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

	retry, err := domain.NewRetryPolicy(cfg.NotifyMaxAttempts, cfg.NotifyRetryDelay)
	if err != nil {
		return err
	}

	if cfg.PlatformBaseURL == "" || cfg.PartnerKey == "" {
		// Коробка, которой некуда и нечем сообщить о смене статуса, поднимается молча
		// сломанной: заказы она принимает и двигает, а покупатель до конца видит «принят».
		// Обнаружилось бы это по жалобе, поэтому служба не поднимается вовсе.
		return errors.New(envPrefix + "PLATFORM_BASE_URL и " + envPrefix +
			"PARTNER_KEY обязательны: без них коробка не сообщит площадке о смене статуса")
	}
	if cfg.NotifyInterval <= 0 {
		// Нулевой интервал — это не «доставлять как можно быстрее», а проход без пауз:
		// коробка занимала бы собой и свою базу, и площадку.
		return errors.New(envPrefix + "NOTIFY_INTERVAL должен быть больше нуля")
	}
	if cfg.AutoAdvanceOrders && cfg.AutoAdvanceInterval <= 0 {
		return errors.New(envPrefix + "AUTO_ADVANCE_INTERVAL должен быть больше нуля")
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
	defer closeQuietly(ctx, log, db)

	router, background := restauranthttp.NewRouter(log, db, restauranthttp.Settings{
		Hours: hours,
		Platform: restauranthttp.PlatformLink{
			BaseURL:    cfg.PlatformBaseURL,
			PartnerKey: cfg.PartnerKey,
			Timeout:    cfg.NotifyTimeout,
		},
		Retry:       retry,
		AutoAdvance: cfg.AutoAdvanceOrders,
	})

	// Уведомления развозятся рядом со службой и заканчиваются вместе с ней — контекст у
	// них общий. Автопилот, если он выключен, таймера не заводит вовсе и сразу выходит.
	go background.Notifications.RunPeriodically(ctx, cfg.NotifyInterval)
	go background.Autopilot.RunPeriodically(ctx, cfg.AutoAdvanceInterval)

	return httpx.Serve(ctx, log, cfg.Config, router)
}

// closeQuietly закрывает подключение к базе: служба к этому моменту уже остановлена, и
// ошибка закрытия ни на что не влияет — но узнать о ней из лога стоит.
func closeQuietly(ctx context.Context, log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.ErrorContext(ctx, "close database", slog.Any("error", err))
	}
}
