package config_test

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
)

// Умолчания службы записаны в трёх местах: в `.env.example` (список ключей для человека),
// в `compose.yaml` (подстановка при запуске) и в коде (запуск без единой переменной). У
// каждого свой потребитель, поэтому свести их в одно место нечем — но разъехаться молча
// они не должны: разошедшееся умолчание означает, что документация обещает не то, с чем
// служба поднимется. Тесты ниже держат эти три места в согласии.

// prefixes — префиксы ключей, за которые отвечает репозиторий. Переменные чужих образов
// (`GOOSE_*`, `POSTGRES_PASSWORD_FILE` и прочие) сюда не попадают.
var prefixes = []string{"POSTGRES_", "PLATFORM_", "RESTAURANT_"}

func TestComposeDefaultsMatchEnvExample(t *testing.T) {
	example := readEnvExample(t)
	compose := readRepoFile(t, "compose.yaml")

	// `${KEY:-значение}` — подстановка Compose вместе с умолчанием; `${KEY}` — без него.
	substitution := regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)(?::-([^}]*))?\}`)

	for _, m := range substitution.FindAllStringSubmatch(compose, -1) {
		key, fallback := m[1], m[2]
		if !ours(key) {
			continue
		}

		documented, ok := example[key]
		if !ok {
			t.Errorf("compose.yaml подставляет %s, которого нет в .env.example", key)
			continue
		}
		if m[2] == "" && !strings.Contains(m[0], ":-") {
			continue // подстановка без умолчания — сверять нечего
		}
		if fallback != documented {
			t.Errorf("умолчание %s разошлось: compose.yaml — %q, .env.example — %q",
				key, fallback, documented)
		}
	}
}

func TestComposeKeysAreDocumented(t *testing.T) {
	example := readEnvExample(t)

	// Ключи, которые compose.yaml задаёт службам явно: `      KEY: значение`.
	assignment := regexp.MustCompile(`(?m)^\s+([A-Z][A-Z0-9_]*):\s`)

	for _, m := range assignment.FindAllStringSubmatch(readRepoFile(t, "compose.yaml"), -1) {
		key := m[1]
		if !ours(key) {
			continue
		}
		if _, ok := example[key]; !ok {
			t.Errorf("compose.yaml задаёт %s, которого нет в .env.example", key)
		}
	}
}

func TestCodeDefaultsMatchEnvExample(t *testing.T) {
	example := readEnvExample(t)

	for _, prefix := range []string{"PLATFORM_", "RESTAURANT_"} {
		defaults := config.Default(0)

		want, ok := example[prefix+"SHUTDOWN_TIMEOUT"]
		if !ok {
			t.Fatalf("в .env.example нет %sSHUTDOWN_TIMEOUT", prefix)
		}
		timeout, err := time.ParseDuration(want)
		if err != nil {
			t.Fatalf("%sSHUTDOWN_TIMEOUT в .env.example: %v", prefix, err)
		}
		if defaults.ShutdownTimeout != timeout {
			t.Errorf("умолчание %sSHUTDOWN_TIMEOUT разошлось: код — %s, .env.example — %s",
				prefix, defaults.ShutdownTimeout, timeout)
		}

		want, ok = example[prefix+"LOG_LEVEL"]
		if !ok {
			t.Fatalf("в .env.example нет %sLOG_LEVEL", prefix)
		}
		var level slog.Level
		if err := level.UnmarshalText([]byte(want)); err != nil {
			t.Fatalf("%sLOG_LEVEL в .env.example: %v", prefix, err)
		}
		if defaults.LogLevel != level {
			t.Errorf("умолчание %sLOG_LEVEL разошлось: код — %s, .env.example — %s",
				prefix, defaults.LogLevel, level)
		}
	}
}

func ours(key string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// readEnvExample разбирает `.env.example` в пары «ключ — значение», пропуская комментарии.
func readEnvExample(t *testing.T) map[string]string {
	t.Helper()

	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(readRepoFile(t, ".env.example")))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf(".env.example: строка без `=`: %q", line)
		}
		values[key] = value
	}

	return values
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	// Тест живёт в internal/pkg/config, а читает файлы корня репозитория.
	content, err := os.ReadFile(filepath.Join("..", "..", "..", name))
	if err != nil {
		t.Fatalf("прочитать %s: %v", name, err)
	}

	return string(content)
}
