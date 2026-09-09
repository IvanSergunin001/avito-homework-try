package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Заведение смотрит в две стороны: витрине оно рассказывает название, интеграции — адрес
// коробки и хеш выданного ключа. Хранилище одно на оба взгляда (это один и тот же ряд в
// `platform.restaurants`), а порты разные — здесь собрана интеграционная сторона.

// partnerColumns — колонки заведения как стороны интеграции, в порядке scanPartner.
// Список один на все запросы: разойдись он с порядком сканирования — и адрес заведения
// уехал бы в его название, а поймалось бы это только на глаз.
const partnerColumns = "id, name, base_url, is_active, created_at, updated_at"

// Add подключает заведение: реквизиты и хеш выданного ему ключа.
//
// Открытый ключ сюда не приезжает вовсе — не «не пишется в запрос», а не доходит до
// хранилища как значение: то, чего нет в адаптере, не попадёт ни в базу, ни в лог
// медленного запроса.
//
// Обратно приезжает то же заведение с метками времени: их ставит база, а не служба, —
// «сейчас» у базы одно на все службы, а у процесса своё.
func (r *Restaurants) Add(
	ctx context.Context, partner domain.Partner, keyHash string,
) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO platform.restaurants (id, name, base_url, api_key_hash, is_active)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+partnerColumns,
		partner.ID, partner.Name, partner.BaseURL, keyHash, partner.IsActive)

	added, err := scanPartner(row)
	if err != nil {
		return domain.Partner{}, fmt.Errorf("подключить заведение %s: %w", partner.ID, err)
	}

	return added, nil
}

// Find находит подключённое заведение по идентификатору — вместе с реквизитами
// интеграции, которых витрина не читает.
//
// Отбора по `is_active` здесь нет намеренно, в отличие от FindByKeyHash: отключённое
// заведение администратор обязан видеть и уметь включить обратно, иначе выключение было
// бы необратимым.
func (r *Restaurants) Find(ctx context.Context, id uuid.UUID) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+partnerColumns+` FROM platform.restaurants WHERE id = $1`, id)

	partner, err := scanPartner(row)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Partner{}, domain.ErrRestaurantNotFound
	case err != nil:
		return domain.Partner{}, fmt.Errorf("найти заведение %s: %w", id, err)
	}

	return partner, nil
}

// Replace заменяет реквизиты подключённого заведения.
//
// Хеш ключа не в списке колонок: переезд заведения на другой адрес — не повод менять его
// секрет, а перевыпуск ключа — отдельное решение, которое администратор принимает
// осознанно, а не заодно с правкой названия.
//
// `updated_at` проставляется здесь же, одним запросом с правкой: отдельным запросом она
// разъехалась бы с правкой на любой неудаче между ними.
func (r *Restaurants) Replace(ctx context.Context, partner domain.Partner) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE platform.restaurants
		SET name = $2, base_url = $3, is_active = $4, updated_at = now()
		WHERE id = $1
		RETURNING `+partnerColumns,
		partner.ID, partner.Name, partner.BaseURL, partner.IsActive)

	changed, err := scanPartner(row)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Partner{}, domain.ErrRestaurantNotFound
	case err != nil:
		return domain.Partner{}, fmt.Errorf("изменить реквизиты заведения %s: %w", partner.ID, err)
	}

	return changed, nil
}

// FindByKeyHash находит подключённое заведение по хешу его ключа.
//
// Отбор по `is_active` стоит в самом запросе: отключённое заведение перестаёт быть
// партнёром площадки целиком, а не только исчезает с витрины, — иначе его ключ продолжал
// бы открывать партнёрские операции после отключения.
func (r *Restaurants) FindByKeyHash(ctx context.Context, keyHash string) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+partnerColumns+`
		FROM platform.restaurants
		WHERE api_key_hash = $1 AND is_active`, keyHash)

	partner, err := scanPartner(row)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Partner{}, domain.ErrRestaurantNotFound
	case err != nil:
		// Хеш в текст ошибки не попадает: сам по себе он API не открывает, но это
		// единственное, что стоит между утёкшим логом и подбором ключа офлайн.
		return domain.Partner{}, fmt.Errorf("найти заведение по ключу: %w", err)
	}

	return partner, nil
}

// scanPartner читает ряд заведения в порядке partnerColumns.
func scanPartner(row *sql.Row) (domain.Partner, error) {
	var partner domain.Partner

	err := row.Scan(&partner.ID, &partner.Name, &partner.BaseURL, &partner.IsActive,
		&partner.CreatedAt, &partner.UpdatedAt)
	if err != nil {
		return domain.Partner{}, err
	}

	return partner, nil
}
