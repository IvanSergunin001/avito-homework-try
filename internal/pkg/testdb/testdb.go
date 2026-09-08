// Package testdb выдаёт тестам настоящий PostgreSQL с накатанными миграциями.
//
// По умолчанию база поднимается контейнером на время прогона, поэтому `go test ./...`
// проходит на чистой машине одной командой: готовить окружение заранее не нужно.
// Две переменные окружения меняют режим:
//
//	TEST_DB_DSN=postgres://user:pass@host:port/db  — не поднимать контейнер, а работать
//	                                                 с базой из окружения запуска;
//	TEST_KEEP_DB=1                                 — не убирать базу после прогона и
//	                                                 напечатать строку подключения, чтобы
//	                                                 заглянуть в данные после прогона.
//
// Вывод прошедшего пакета `go test` без `-v` глотает целиком, поэтому в режиме
// TEST_KEEP_DB строку подключения видно только с `-v`: `make test` добавляет флаг сам,
// когда переменная выставлена.
//
// Отладчик работает в любом режиме: это обычный `go test`, никакой обёртки вокруг него
// оснастка не заводит и в отдельном процессе ничего не запускает.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql, которым ходят тесты
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	platformmigrations "github.com/IvanSergunin001/Test157/internal/platform/migrations"
	boxmigrations "github.com/IvanSergunin001/Test157/internal/restaurant/migrations"
)

const (
	// DSNEnv — переменная, переключающая тесты на внешнюю базу вместо контейнера.
	// Значение — строка подключения вида `postgres://user:pass@host:port/db`.
	DSNEnv = "TEST_DB_DSN"
	// KeepEnv — переменная, оставляющая базу после прогона: оснастка не удаляет её и
	// печатает строку подключения.
	KeepEnv = "TEST_KEEP_DB"

	// image — тот же образ базы, что и в Compose: тесты не должны проходить на версии
	// PostgreSQL, которой нет в запуске.
	image = "postgres:18-alpine"
	// credential — пользователь, пароль и база контейнера. Секрета здесь нет: контейнер
	// живёт минуты и слушает случайный порт.
	credential = "kitchen"

	startTimeout   = 2 * time.Minute
	connectTimeout = 15 * time.Second
	dropTimeout    = 30 * time.Second

	// nameLimit — сколько знаков имени теста попадает в имя базы. Предел PostgreSQL на
	// идентификатор — 63 байта, и остаток нужен префиксу и случайному хвосту.
	nameLimit = 32
)

// DB — база, выданная одному тесту. Своя база у каждого теста: тесты не видят следов друг
// друга и могут идти параллельно. Встроенный `*sql.DB` даёт обычные методы database/sql.
type DB struct {
	*sql.DB
	// DSN — строка подключения именно к этой базе: её получает служба, которую тест
	// поднимает по своему шву.
	DSN string
}

// New создаёт тесту свежую базу с накатанными миграциями обоих контекстов и возвращает
// подключение к ней. Базу оснастка убирает за собой сама — кроме режима TEST_KEEP_DB,
// в котором она остаётся вместе с напечатанной строкой подключения.
func New(t *testing.T) *DB {
	t.Helper()

	name := databaseName(t)
	instance := serverDSN(t)
	dsn := databaseDSN(t, instance, name)

	admin := connect(t, instance)
	// Имя базы — идентификатор, а не значение, и параметром запроса не передаётся;
	// собирает его databaseName из имени теста и случайного хвоста.
	_, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`)
	closeQuietly(admin)

	if err != nil {
		t.Fatalf("создать базу %s: %v", name, err)
	}

	t.Cleanup(func() {
		if keepDatabase() {
			t.Logf("база %s оставлена после прогона (%s=1): %s", name, KeepEnv, dsn)

			return
		}
		dropDatabase(t, name)
	})

	db := connect(t, dsn)
	t.Cleanup(func() { closeQuietly(db) })

	migrateUp(t, db)

	return &DB{DB: db, DSN: dsn}
}

// migrationSet — миграции одного контекста: те же файлы, что накатывает шаг запуска, и
// журнал, в котором отмечены применённые.
type migrationSet struct {
	name         string
	files        fs.FS
	versionTable string
}

// migrationSets перечисляет контексты в том же порядке, в каком их накатывает шаг запуска.
//
// Тот же перечень повторяется там, где Go не дотягивается: в скрипте шага миграций и в
// заготовке новой миграции (`Dockerfile`, цели `migrate` и `migration-draft`), в значении
// по умолчанию для `context` (`Makefile`) и в таблице контекстов (`README.md`). Третий
// контекст или переименование правятся во всех этих местах разом.
var migrationSets = []migrationSet{
	{name: "площадка", files: platformmigrations.FS, versionTable: platformmigrations.VersionTable},
	{name: "коробка заведения", files: boxmigrations.FS, versionTable: boxmigrations.VersionTable},
}

// migrateUp накатывает миграции обоих контекстов. Схему тесты не воспроизводят руками:
// расхождение между тем, что накатано в тестах, и тем, что накатано при запуске, — самый
// дорогой вид зелёных тестов.
func migrateUp(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, set := range migrationSets {
		provider, err := goose.NewProvider(goose.DialectPostgres, db, set.files, goose.WithTableName(set.versionTable))
		if err != nil {
			t.Fatalf("подготовить миграции (%s): %v", set.name, err)
		}
		if _, err := provider.Up(t.Context()); err != nil {
			t.Fatalf("накатить миграции (%s): %v", set.name, err)
		}
	}
}

var (
	serverOnce sync.Once
	server     string
	serverErr  error
)

// serverDSN возвращает строку подключения к инстансу PostgreSQL, в котором оснастка
// заводит базы. Инстанс на процесс тестов один: контейнер поднимается при первом
// обращении, а внешняя база берётся из окружения как есть.
func serverDSN(t *testing.T) string {
	t.Helper()

	serverOnce.Do(startServer)

	if serverErr != nil {
		t.Fatalf("PostgreSQL для тестов: %v", serverErr)
	}

	return server
}

// startServer поднимает контейнер с базой — или не поднимает ничего, если тесты просят
// внешнюю базу. Контейнер убирает за собой сборщик testcontainers, когда процесс тестов
// заканчивается; в режиме TEST_KEEP_DB сборщик выключен и контейнер остаётся.
func startServer() {
	if dsn := os.Getenv(DSNEnv); dsn != "" {
		server = dsn

		return
	}

	if keepDatabase() {
		if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
			serverErr = fmt.Errorf("выключить сборщик контейнеров: %w", err)

			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase(credential),
		postgres.WithUsername(credential),
		postgres.WithPassword(credential),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		serverErr = fmt.Errorf("поднять контейнер с PostgreSQL: %w", err)

		return
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		serverErr = fmt.Errorf("получить строку подключения к контейнеру: %w", err)

		return
	}
	server = dsn

	if keepDatabase() {
		fmt.Fprintf(os.Stderr, "PostgreSQL оставлен после прогона: %s\nубрать: docker rm -f %s\n",
			dsn, container.GetContainerID())
	}
}

// keepDatabase сообщает, просили ли оставить базу после прогона.
func keepDatabase() bool {
	value := os.Getenv(KeepEnv)

	return value != "" && value != "0" && value != "false"
}

// databaseName собирает имя базы из имени теста и случайного хвоста: по имени видно, чей
// это след, когда базу оставили после падения, а хвост разводит параллельные тесты и
// повторные прогоны.
func databaseName(t *testing.T) string {
	readable := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		default:
			return '_'
		}
	}, t.Name())

	if len(readable) > nameLimit {
		readable = readable[:nameLimit]
	}

	return fmt.Sprintf("test_%s_%s", readable, strings.ToLower(rand.Text()[:8]))
}

// databaseDSN подменяет базу в строке подключения к инстансу.
func databaseDSN(t *testing.T, serverDSN, name string) string {
	t.Helper()

	parsed, err := url.Parse(serverDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("строка подключения к инстансу должна быть вида postgres://user:pass@host:port/db, получили %q",
			masked(serverDSN))
	}
	parsed.Path = "/" + name

	return parsed.String()
}

// connect открывает подключение и убеждается, что база отвечает.
func connect(t *testing.T, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("открыть подключение к %s: %v", masked(dsn), err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), connectTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		closeQuietly(db)
		t.Fatalf("база не отвечает по %s: %v", masked(dsn), err)
	}

	return db
}

// dropDatabase удаляет базу теста. Подключение открывается заново: контекст теста к этому
// моменту уже отменён, а базу нужно убрать в любом случае.
func dropDatabase(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
	defer cancel()

	admin, err := sql.Open("pgx", server)
	if err != nil {
		t.Errorf("убрать базу %s: %v", name, err)

		return
	}
	defer closeQuietly(admin)

	// FORCE отключает подключения, которые тест не закрыл: недосмотр в тесте не должен
	// оставлять за собой базы.
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
		t.Errorf("убрать базу %s: %v", name, err)
	}
}

// masked прячет пароль в строке подключения. DSN попадает в сообщения об ошибках, а с
// внешней базой (TEST_DB_DSN) пароль в ней настоящий: в логе прогона ему не место.
// Строка подключения, которую печатает режим TEST_KEEP_DB, маскировке не подлежит — по
// ней в базу как раз и заходят руками.
func masked(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "строка подключения неразборчива"
	}

	if _, set := parsed.User.Password(); set {
		parsed.User = url.UserPassword(parsed.User.Username(), "***")
	}

	return parsed.String()
}

// closeQuietly закрывает подключение: закрывать нечего, кроме как молча, — ошибка закрытия
// уже ни на что не влияет.
func closeQuietly(db *sql.DB) {
	_ = db.Close()
}
