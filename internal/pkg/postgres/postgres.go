// Package postgres открывает подключение к PostgreSQL. Пул настраивается одинаково для
// обеих служб: подключение к базе — это инфраструктура, а не свойство площадки или
// коробки, и второй экземпляр тех же настроек разъехался бы с первым первой же правкой.
//
// Драйвер подключается здесь, поэтому службы о том, чем именно они ходят в базу, не знают.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql, которым службы ходят в PostgreSQL
)

const (
	// maxOpenConns — потолок на подключения к базе. Он есть у каждой службы: без него
	// всплеск запросов упирается в лимит подключений самой базы, и первым падает не
	// перегруженный обработчик, а всё остальное, что в эту базу ходит.
	maxOpenConns = 20
	// maxIdleConns — сколько подключений держим открытыми в простое.
	maxIdleConns = 5
	// connMaxIdleTime — сколько живёт незанятое подключение: база за ним не следит, а
	// оборванное сетью подключение иначе достаётся первому же запросу.
	connMaxIdleTime = 5 * time.Minute

	// pingTimeout — сколько ждём ответа базы на старте службы.
	pingTimeout = 5 * time.Second
)

// Open открывает пул подключений к базе и убеждается, что база отвечает.
//
// Проверка на старте намеренная: служба, поднявшаяся с недоступной базой, отвечает на
// пробу здоровья «ок» и получает трафик, который ей нечем обслужить.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("строка подключения к базе не задана")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("подключиться к базе: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("база не отвечает: %w", err)
	}

	return db, nil
}

// CloseQuietly закрывает подключение к базе: служба к этому моменту уже остановлена, и
// ошибка закрытия ни на что не влияет — но узнать о ней из лога стоит.
func CloseQuietly(ctx context.Context, log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.ErrorContext(ctx, "close database", slog.Any("error", err))
	}
}
