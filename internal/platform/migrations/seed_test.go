// Демонстрационные данные — отдельный набор миграций, и тестам они не накатываются:
// каждый тест, который считает заведения, считал бы вместе со своими ещё и чужое. Проверяем
// их отдельно — здесь, накатив набор на базу теста руками, как это делает шаг запуска.
package migrations_test

import (
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/IvanSergunin001/Test157/internal/pkg/testdb"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
	"github.com/IvanSergunin001/Test157/internal/platform/migrations"
)

// demoPartnerKey — ключ демо-заведения, напечатанный в самой seed-миграции и в README.
// Он известен всем и годится только для демонстрации.
const demoPartnerKey = "pk_DEMOKEYDEMOKEYDEMOKEYDEMOK"

func TestDemoRestaurantIsConnectedRightAfterStartAndAnswersByItsKey(t *testing.T) {
	t.Parallel()

	db := testdb.New(t)
	applySeed(t, db)

	var (
		name     string
		baseURL  string
		keyHash  string
		isActive bool
	)
	row := db.QueryRowContext(t.Context(), `
		SELECT name, base_url, api_key_hash, is_active
		FROM platform.restaurants
		WHERE api_key_hash = $1`, domain.HashPartnerKey(demoPartnerKey))
	if err := row.Scan(&name, &baseURL, &keyHash, &isActive); err != nil {
		t.Fatalf("демо-заведение не подключилось seed-миграцией: %v", err)
	}

	// Ключ в базе лежит хешем, как у всякого заведения: демонстрационное отличается тем,
	// что его ключ напечатан в документации, а не тем, что для него сделано исключение.
	if keyHash == demoPartnerKey {
		t.Error("ключ демо-заведения лежит в базе открытым")
	}
	if !isActive {
		t.Error("демо-заведение выключено, а заводится оно затем, чтобы система работала сразу после запуска")
	}
	// Адрес — это адрес коробки, которая поднимается рядом в Compose: без него площадке
	// некуда стучаться, и «рабочая сразу после запуска» система оказалась бы рабочей
	// только на витрине.
	if baseURL != "http://restaurant:8081" {
		t.Errorf("адрес демо-заведения %q, ожидали адрес коробки из Compose", baseURL)
	}
	if name == "" {
		t.Error("у демо-заведения нет названия, а покупатель видит на витрине именно его")
	}
}

// applySeed накатывает демонстрационные данные тем же инструментом и с тем же журналом,
// какими их накатывает шаг запуска: тест не воспроизводит их руками, иначе он проверял бы
// не то, что доедет до запущенной системы.
func applySeed(t *testing.T, db *testdb.DB) {
	t.Helper()

	files, err := fs.Sub(migrations.SeedFS, migrations.SeedDir)
	if err != nil {
		t.Fatalf("открыть демонстрационные данные: %v", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db.DB, files,
		goose.WithTableName(migrations.SeedVersionTable))
	if err != nil {
		t.Fatalf("подготовить демонстрационные данные: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("накатить демонстрационные данные: %v", err)
	}
}
