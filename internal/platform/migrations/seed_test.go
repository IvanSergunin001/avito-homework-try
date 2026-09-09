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

// demoRestaurantID — идентификатор демо-заведения, объявленный самой seed-миграцией.
// Ряд выбирается по нему, а не по хешу ключа: выбор по хешу делал бы проверку хранения
// ключа мёртвой — найденный ряд по построению равен искомому хешу и открытым ключом не
// бывает никогда.
const demoRestaurantID = "018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a01"

// Имя теста называет ровно то, что он проверяет: что демо-заведение легло в базу и что
// его ключ лежит там хешем. «Отвечает по своему ключу» проверяется не здесь — до роутера
// этот пакет не дотягивается, а швом площадки демо-данные намеренно не накатываются.
func TestDemoRestaurantIsConnectedRightAfterStartAndItsKeyIsStoredHashed(t *testing.T) {
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
		WHERE id = $1`, demoRestaurantID)
	if err := row.Scan(&name, &baseURL, &keyHash, &isActive); err != nil {
		t.Fatalf("демо-заведение не подключилось seed-миграцией: %v", err)
	}

	// Ключ в базе лежит хешем, как у всякого заведения: демонстрационное отличается тем,
	// что его ключ напечатан в документации, а не тем, что для него сделано исключение.
	// Сверяется он с хешем напечатанного ключа, а не «не равен открытому»: связь между
	// тем, что напечатано в README, и тем, что лежит в базе, — это и есть проверяемое.
	if want := domain.HashPartnerKey(demoPartnerKey); keyHash != want {
		t.Errorf("в базе %q, ожидали хеш напечатанного ключа %q: по нему площадка опознаёт демо-заведение",
			keyHash, want)
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

// Синхронизация каталога пишет демо-заведению меню, а меню держит заведение: удалить
// заведение, у которого есть позиции, база не даёт — внешний ключ товара запрещающий.
// Поэтому откат сида проверяется не на чистой базе, а после того, как каталог хоть раз
// синхронизировался: до этого слайса откатывать было нечего, а с него — есть что, и
// сломанный откат нашёл бы не тест, а человек, сворачивающий демонстрацию.
func TestDemoRestaurantRollsBackAfterTheCatalogHasBeenSynced(t *testing.T) {
	t.Parallel()

	db := testdb.New(t)
	applySeed(t, db)

	// Позиция меню — то, что оставляет после себя синхронизация каталога: сид её не
	// заводит, поэтому и кладём её сюда сами, а не ждём от сида.
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO platform.products (id, restaurant_id, external_id, name, price)
		SELECT $1, id, $2, $3, $4 FROM platform.restaurants`,
		"018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a93", "baget", "Багет", 12000); err != nil {
		t.Fatalf("положить позицию меню демо-заведению: %v", err)
	}

	if _, err := seedProvider(t, db).Down(t.Context()); err != nil {
		t.Fatalf("откатить демонстрационные данные: %v", err)
	}

	var restaurants int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM platform.restaurants`).Scan(&restaurants); err != nil {
		t.Fatalf("пересчитать заведения: %v", err)
	}
	if restaurants != 0 {
		t.Errorf("после отката осталось заведений: %d, ожидали 0", restaurants)
	}
}

// applySeed накатывает демонстрационные данные тем же инструментом и с тем же журналом,
// какими их накатывает шаг запуска: тест не воспроизводит их руками, иначе он проверял бы
// не то, что доедет до запущенной системы.
func applySeed(t *testing.T, db *testdb.DB) {
	t.Helper()

	if _, err := seedProvider(t, db).Up(t.Context()); err != nil {
		t.Fatalf("накатить демонстрационные данные: %v", err)
	}
}

// seedProvider собирает goose поверх набора демонстрационных данных — тот же набор и тот
// же журнал, что у шага запуска.
func seedProvider(t *testing.T, db *testdb.DB) *goose.Provider {
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

	return provider
}
