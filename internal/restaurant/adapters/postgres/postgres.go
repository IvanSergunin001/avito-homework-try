// Package postgres — хранилище коробки заведения: реализация портов сценариев поверх
// схемы `restaurant`. Драйвер подключается здесь и только здесь, поэтому остальной код
// коробки о том, какой базой он обслуживается, не знает.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql, которым коробка ходит в PostgreSQL
)

const (
	// maxOpenConns — потолок на подключения к базе. Он есть у каждой службы: без него
	// всплеск запросов упирается в лимит подключений самой базы, и первым падает не
	// перегруженный обработчик, а всё остальное, что в эту базу ходит.
	//
	// Настройки пула у коробки свои, а не общие с площадкой, хотя значения пока совпадают:
	// коробка — другая организация и в эксплуатации другой инстанс базы, и подбирать её
	// пул придётся по её собственной нагрузке.
	maxOpenConns = 20
	// maxIdleConns — сколько подключений держим открытыми в простое.
	maxIdleConns = 5
	// connMaxIdleTime — сколько живёт незанятое подключение: база за ним не следит, а
	// оборванное сетью подключение иначе достаётся первому же запросу.
	connMaxIdleTime = 5 * time.Minute

	// pingTimeout — сколько ждём ответа базы на старте службы.
	pingTimeout = 5 * time.Second
)

// Open открывает пул подключений к базе коробки и убеждается, что база отвечает.
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
