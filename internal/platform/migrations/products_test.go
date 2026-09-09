// Правило «заведение с меню не удаляется» держит не код площадки, а внешний ключ в схеме:
// ручки удаления заведения в API нет, и единственный, кто доберётся до `DELETE`, — человек
// с psql. Значит, и проверять правило надо там же, где оно живёт, — на настоящей базе с
// накатанными миграциями.
package migrations_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
)

// SQLSTATE, которыми PostgreSQL отвечает на попытку удалить строку, на которую ссылаются:
// `RESTRICT` проверяет ключ сразу и отвечает `restrict_violation`, отложенная проверка
// умолчания (`NO ACTION`) — `foreign_key_violation`. Тесту важно, что удаление отклонено,
// а не какой из двух проверок; коды перечислены затем, чтобы он не остался зелёным на
// опечатке в запросе.
const (
	restrictViolation   = "23001"
	foreignKeyViolation = "23503"
)

func TestRestaurantWithAMenuIsNotDeleted(t *testing.T) {
	t.Parallel()

	db := testdb.New(t)

	const (
		restaurantID = "018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a91"
		productID    = "018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a92"
	)

	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO platform.restaurants (id, name) VALUES ($1, $2)`,
		restaurantID, "Пекарня «Батон»"); err != nil {
		t.Fatalf("завести заведение: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO platform.products (id, restaurant_id, name, price) VALUES ($1, $2, $3, $4)`,
		productID, restaurantID, "Багет", 12000); err != nil {
		t.Fatalf("завести позицию меню: %v", err)
	}

	_, err := db.ExecContext(t.Context(), `DELETE FROM platform.restaurants WHERE id = $1`, restaurantID)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || (pgErr.Code != restrictViolation && pgErr.Code != foreignKeyViolation) {
		t.Fatalf("заведение с меню удалилось (ошибка: %v), а ключ должен был это запретить", err)
	}

	// Меню на месте — ради этого ключ и запрещающий: каскад унёс бы его молча.
	var products int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM platform.products WHERE restaurant_id = $1`, restaurantID).Scan(&products); err != nil {
		t.Fatalf("пересчитать меню: %v", err)
	}
	if products != 1 {
		t.Errorf("после отклонённого удаления в меню %d позиций, ожидали 1", products)
	}

	// А без меню заведение удаляется: держит его именно меню, а не что-то ещё в схеме.
	if _, err := db.ExecContext(t.Context(), `DELETE FROM platform.products WHERE restaurant_id = $1`, restaurantID); err != nil {
		t.Fatalf("убрать меню: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM platform.restaurants WHERE id = $1`, restaurantID); err != nil {
		t.Fatalf("удалить заведение без меню: %v", err)
	}
}
