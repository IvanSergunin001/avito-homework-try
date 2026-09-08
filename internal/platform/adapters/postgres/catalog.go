package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Catalog — каталог площадки со стороны синхронизации: сюда приезжает меню, забранное у
// заведения.
//
// Хранилище отдельное от Products, хотя таблица за ними одна: витрине нужны товары
// работающих заведений и ничего больше, синхронизации — право переписать меню целиком.
// Один порт на оба сценария означал бы, что чтение витрины умеет деактивировать позиции.
type Catalog struct {
	db *sql.DB
}

// NewCatalog собирает каталог поверх подключения к базе площадки.
func NewCatalog(db *sql.DB) *Catalog {
	return &Catalog{db: db}
}

// Replace применяет выгрузку заведения целиком.
//
// Всё происходит в одной транзакции, и это не оптимизация, а само требование: сбой между
// обновлением позиций и деактивацией исчезнувших оставил бы на витрине меню, которого нет
// ни у заведения, ни у площадки до синхронизации. Откат возвращает прежний каталог
// целиком, и покупатель разницы не замечает вовсе.
func (c *Catalog) Replace(
	ctx context.Context, restaurant uuid.UUID, items []domain.CatalogItem, syncedAt time.Time,
) error {
	arrived, err := arrivedMenu(items)
	if err != nil {
		return err
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начать обновление каталога заведения %s: %w", restaurant, err)
	}
	// Откат после успешного коммита — пустая операция, поэтому отдельного флага «уже
	// закоммичено» не нужно: сорванный по дороге вызов откатывает транзакцию сам.
	defer func() { _ = tx.Rollback() }()

	if err := upsertArrived(ctx, tx, restaurant, arrived, syncedAt); err != nil {
		return err
	}
	if err := deactivateVanished(ctx, tx, restaurant, arrived.externalIDs, syncedAt); err != nil {
		return err
	}
	if err := markSynced(ctx, tx, restaurant, syncedAt); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("применить каталог заведения %s: %w", restaurant, err)
	}

	return nil
}

// arrivedItems — выгрузка, разложенная по колонкам: базе она уезжает шестью массивами,
// а не строкой за строкой.
//
// Один запрос вместо запроса на позицию — не ради красоты: меню в несколько сотен позиций
// означало бы столько же обращений к базе внутри транзакции, и синхронизация одного
// заведения держала бы подключение в разы дольше, чем нужно.
type arrivedItems struct {
	ids          []string
	externalIDs  []string
	names        []string
	descriptions []string
	prices       []int64
	available    []bool
}

// arrivedMenu раскладывает выгрузку по колонкам и выдаёт идентификаторы новым позициям.
//
// Идентификатор выдаётся каждой приехавшей позиции, но достаётся только новой: у уже
// известной он остался с прошлой синхронизации, и менять его нельзя — по нему открыта
// карточка товара, на которую покупатель мог сохранить ссылку. uuid v7 монотонный, и
// вставка новых позиций не разносит первичный индекс.
func arrivedMenu(items []domain.CatalogItem) (arrivedItems, error) {
	arrived := arrivedItems{
		ids:          make([]string, 0, len(items)),
		externalIDs:  make([]string, 0, len(items)),
		names:        make([]string, 0, len(items)),
		descriptions: make([]string, 0, len(items)),
		prices:       make([]int64, 0, len(items)),
		available:    make([]bool, 0, len(items)),
	}

	for _, item := range items {
		id, err := uuid.NewV7()
		if err != nil {
			return arrivedItems{}, fmt.Errorf("выдать идентификатор товару %q: %w", item.ExternalID, err)
		}

		arrived.ids = append(arrived.ids, id.String())
		arrived.externalIDs = append(arrived.externalIDs, item.ExternalID)
		arrived.names = append(arrived.names, item.Name)
		arrived.descriptions = append(arrived.descriptions, item.Description)
		arrived.prices = append(arrived.prices, int64(item.Price))
		arrived.available = append(arrived.available, item.IsAvailable)
	}

	return arrived, nil
}

// upsertArrived заводит приехавшие позиции и обновляет уже известные.
//
// Ключ обновления — пара «заведение + внешний идентификатор»: другого у площадки нет,
// свой идентификатор заведение не знает, а по названию позиции сводить каталог значило бы
// заводить новую позицию при каждом исправлении опечатки.
//
// Вернувшаяся позиция снова становится активной тем же запросом: заведение, которое
// убрало блюдо на неделю и вернуло, ждёт его на витрине, а не второй карточки рядом.
func upsertArrived(
	ctx context.Context, tx *sql.Tx, restaurant uuid.UUID, arrived arrivedItems, syncedAt time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO platform.products
			(id, restaurant_id, external_id, name, description, price, is_available, is_active, updated_at)
		SELECT arrived.id, $1, arrived.external_id, arrived.name, arrived.description,
		       arrived.price, arrived.is_available, true, $8
		FROM unnest($2::uuid[], $3::text[], $4::text[], $5::text[], $6::bigint[], $7::boolean[])
		     AS arrived(id, external_id, name, description, price, is_available)
		ON CONFLICT (restaurant_id, external_id) DO UPDATE
		SET name         = excluded.name,
		    description  = excluded.description,
		    price        = excluded.price,
		    is_available = excluded.is_available,
		    is_active    = true,
		    updated_at   = excluded.updated_at`,
		restaurant, arrived.ids, arrived.externalIDs, arrived.names,
		arrived.descriptions, arrived.prices, arrived.available, syncedAt)
	if err != nil {
		return fmt.Errorf("обновить позиции каталога заведения %s: %w", restaurant, err)
	}

	return nil
}

// deactivateVanished убирает с витрины позиции, которых в выгрузке не оказалось.
//
// Деактивация, а не удаление: строка помнит, что позиция была, и вернувшееся назавтра
// блюдо получает ту же карточку, а не новую ссылку взамен разосланной. Пустая выгрузка
// деактивирует всё меню — это не защита от ошибки заведения, а её точная передача:
// заведение сказало, что в меню ничего нет.
func deactivateVanished(
	ctx context.Context, tx *sql.Tx, restaurant uuid.UUID, arrived []string, syncedAt time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE platform.products
		SET is_active = false, updated_at = $3
		WHERE restaurant_id = $1 AND is_active AND NOT (external_id = ANY($2::text[]))`,
		restaurant, arrived, syncedAt)
	if err != nil {
		return fmt.Errorf("деактивировать исчезнувшие позиции заведения %s: %w", restaurant, err)
	}

	return nil
}

// markSynced отмечает время, когда меню заведения забрали в последний раз.
//
// Отметка ставится в той же транзакции, что и само меню: время синхронизации, пережившее
// откат каталога, утверждало бы, что меню обновлено, тогда как на витрине лежит прежнее.
func markSynced(ctx context.Context, tx *sql.Tx, restaurant uuid.UUID, syncedAt time.Time) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE platform.restaurants SET last_synced_at = $2 WHERE id = $1`, restaurant, syncedAt)
	if err != nil {
		return fmt.Errorf("отметить синхронизацию заведения %s: %w", restaurant, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("отметить синхронизацию заведения %s: %w", restaurant, err)
	}
	if changed == 0 {
		return domain.ErrRestaurantNotFound
	}

	return nil
}
