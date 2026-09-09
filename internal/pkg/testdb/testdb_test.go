package testdb_test

import (
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
)

func TestEachBoundedContextOwnsItsOwnSchemaInTheSharedInstance(t *testing.T) {
	t.Parallel()

	db := testdb.New(t)

	for _, schema := range []string{"platform", "restaurant"} {
		var found int
		row := db.QueryRowContext(t.Context(),
			`SELECT count(*) FROM information_schema.schemata WHERE schema_name = $1`, schema)
		if err := row.Scan(&found); err != nil {
			t.Fatalf("спросить о схеме %s: %v", schema, err)
		}
		if found != 1 {
			t.Errorf("схемы %s нет в базе, а миграции должны её заводить", schema)
		}
	}
}

func TestPlatformAndRestaurantBoxAreNotLinkedByASingleForeignKey(t *testing.T) {
	t.Parallel()

	db := testdb.New(t)

	rows, err := db.QueryContext(t.Context(), `
		SELECT c.conname, child_schema.nspname, parent_schema.nspname
		FROM pg_constraint c
		JOIN pg_class child ON child.oid = c.conrelid
		JOIN pg_namespace child_schema ON child_schema.oid = child.relnamespace
		JOIN pg_class parent ON parent.oid = c.confrelid
		JOIN pg_namespace parent_schema ON parent_schema.oid = parent.relnamespace
		WHERE c.contype = 'f'
		  AND child_schema.nspname <> parent_schema.nspname
		  AND child_schema.nspname IN ('platform', 'restaurant')
		  AND parent_schema.nspname IN ('platform', 'restaurant')`)
	if err != nil {
		t.Fatalf("спросить о внешних ключах между схемами: %v", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name, child, parent string
		if err := rows.Scan(&name, &child, &parent); err != nil {
			t.Fatalf("прочитать внешний ключ: %v", err)
		}
		t.Errorf("внешний ключ %s связал схему %s со схемой %s: граница контекстов должна быть физической",
			name, child, parent)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("перебрать внешние ключи: %v", err)
	}
}

func TestOneTestDoesNotSeeDataOfAnother(t *testing.T) {
	t.Parallel()

	first, second := testdb.New(t), testdb.New(t)

	if _, err := first.ExecContext(t.Context(), `CREATE TABLE platform.scratch (id int)`); err != nil {
		t.Fatalf("завести таблицу в первой базе: %v", err)
	}

	var visible bool
	row := second.QueryRowContext(t.Context(), `SELECT to_regclass('platform.scratch') IS NOT NULL`)
	if err := row.Scan(&visible); err != nil {
		t.Fatalf("спросить вторую базу о таблице: %v", err)
	}
	if visible {
		t.Error("вторая база видит таблицу, заведённую в первой: у каждого теста своя база")
	}
	if first.DSN == second.DSN {
		t.Errorf("обе базы отвечают по одной строке подключения %s", first.DSN)
	}
}
