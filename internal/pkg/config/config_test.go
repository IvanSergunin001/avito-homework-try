package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
)

func TestVariableWithoutServicePrefixDoesNotAffectService(t *testing.T) {
	t.Setenv("HTTP_PORT", "9999")
	t.Setenv("OTHER_HTTP_PORT", "7777")

	cfg := config.Default(8080)
	if err := config.Load("PLATFORM_", &cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, ожидали значение по умолчанию 8080", cfg.HTTPPort)
	}
}

func TestEachServiceReadsOnlyItsOwnPrefix(t *testing.T) {
	t.Setenv("PLATFORM_HTTP_PORT", "8080")
	t.Setenv("RESTAURANT_HTTP_PORT", "8081")

	platform := config.Default(0)
	if err := config.Load("PLATFORM_", &platform); err != nil {
		t.Fatalf("load platform config: %v", err)
	}

	restaurant := config.Default(0)
	if err := config.Load("RESTAURANT_", &restaurant); err != nil {
		t.Fatalf("load restaurant config: %v", err)
	}

	if platform.HTTPPort != 8080 || restaurant.HTTPPort != 8081 {
		t.Errorf("получили площадку на порту %d и коробку на порту %d, ожидали 8080 и 8081",
			platform.HTTPPort, restaurant.HTTPPort)
	}
}

func TestSettingIsTakenFromEnvironment(t *testing.T) {
	t.Setenv("PLATFORM_HTTP_PORT", "9000")
	t.Setenv("PLATFORM_SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("PLATFORM_LOG_LEVEL", "debug")

	cfg := config.Default(8080)
	if err := config.Load("PLATFORM_", &cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := config.Config{
		HTTPPort:        9000,
		ShutdownTimeout: 3 * time.Second,
		LogLevel:        slog.LevelDebug,
	}
	if cfg != want {
		t.Errorf("получили %+v, ожидали %+v", cfg, want)
	}
	if cfg.Addr() != ":9000" {
		t.Errorf("Addr() = %q, ожидали \":9000\"", cfg.Addr())
	}
}

func TestUnsetSettingKeepsItsDefault(t *testing.T) {
	t.Setenv("PLATFORM_LOG_LEVEL", "warn")

	cfg := config.Default(8080)
	if err := config.Load("PLATFORM_", &cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, ожидали значение по умолчанию 10s", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, ожидали warn", cfg.LogLevel)
	}
}

func TestUnknownVariableOfTheServiceIsRefused(t *testing.T) {
	t.Setenv("PLATFORM_HTTP_PORTS", "9000")

	cfg := config.Default(8080)
	if err := config.Load("PLATFORM_", &cfg); err == nil {
		t.Fatalf("опечатка в имени переменной принята молча, конфигурация: %+v", cfg)
	}
}

func TestSettingWithWrongValueIsRefused(t *testing.T) {
	t.Setenv("PLATFORM_SHUTDOWN_TIMEOUT", "десять секунд")

	cfg := config.Default(8080)
	if err := config.Load("PLATFORM_", &cfg); err == nil {
		t.Fatalf("нечисловая длительность принята молча, конфигурация: %+v", cfg)
	}
}
