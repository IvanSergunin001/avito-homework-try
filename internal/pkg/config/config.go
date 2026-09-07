// Package config читает конфигурацию служб из переменных окружения.
//
// Других источников (файлов, флагов, сервисов конфигурации) сознательно нет: единственный
// источник истины — окружение процесса, а подстановкой `.env` занимается Docker Compose.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

// Service — настройки, общие для площадки и коробки заведения. Каждая служба читает их
// под своим префиксом (`PLATFORM_`, `RESTAURANT_`), поэтому обе живут в одном окружении
// Compose и не мешают друг другу.
type Service struct {
	// HTTPPort — порт, который слушает HTTP-сервер службы. Компоуз публикует его на
	// хост под тем же номером, поэтому порт — единственный ключ, а не пара «адрес + порт».
	HTTPPort int `koanf:"http_port"`
	// ShutdownTimeout — сколько ждём завершения начатых запросов после сигнала остановки.
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	// LogLevel — минимальный уровень записи: debug, info, warn, error.
	LogLevel slog.Level `koanf:"log_level"`
}

// DefaultService — значения по умолчанию для службы, слушающей httpPort. Их переопределяет
// окружение; в коде они нужны, чтобы бинарь запускался без единой переменной.
func DefaultService(httpPort int) Service {
	return Service{
		HTTPPort:        httpPort,
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        slog.LevelInfo,
	}
}

// Addr — адрес для http.Server: служба слушает все интерфейсы контейнера.
func (s Service) Addr() string {
	return net.JoinHostPort("", strconv.Itoa(s.HTTPPort))
}

// Load разбирает переменные окружения с префиксом prefix (например, `PLATFORM_`) в dst.
// Значения полей dst до вызова служат значениями по умолчанию: переменная, которой нет
// в окружении, поле не трогает.
//
// Переменная с этим префиксом, которой не соответствует ни одно поле, — ошибка: иначе
// опечатка в имени ключа молча оставила бы значение по умолчанию, и служба поднялась бы
// не с той конфигурацией, с какой её просили поднять.
func Load(prefix string, dst any) error {
	k := koanf.New(".")

	provider := env.Provider(".", env.Opt{
		Prefix: prefix,
		TransformFunc: func(key, value string) (string, any) {
			return strings.ToLower(strings.TrimPrefix(key, prefix)), value
		},
	})
	if err := k.Load(provider, nil); err != nil {
		return fmt.Errorf("read environment with prefix %q: %w", prefix, err)
	}

	err := k.UnmarshalWithConf("", dst, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
			ErrorUnused:      true,
		},
	})
	if err != nil {
		return fmt.Errorf("parse environment with prefix %q: %w", prefix, err)
	}

	return nil
}
