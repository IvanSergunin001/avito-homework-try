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

// Add подключает заведение: реквизиты и хеш выданного ему ключа.
//
// Открытый ключ сюда не приезжает вовсе — не «не пишется в запрос», а не доходит до
// хранилища как значение: то, чего нет в адаптере, не попадёт ни в базу, ни в лог
// медленного запроса.
func (r *Restaurants) Add(ctx context.Context, partner domain.Partner, keyHash string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO platform.restaurants (id, name, base_url, api_key_hash, is_active)
		VALUES ($1, $2, $3, $4, $5)`,
		partner.ID, partner.Name, partner.BaseURL, keyHash, partner.IsActive)
	if err != nil {
		return fmt.Errorf("подключить заведение %s: %w", partner.ID, err)
	}

	return nil
}

// Replace заменяет реквизиты подключённого заведения.
//
// Хеш ключа не в списке колонок: переезд заведения на другой адрес — не повод менять его
// секрет, а перевыпуск ключа — отдельное решение, которое администратор принимает
// осознанно, а не заодно с правкой названия.
func (r *Restaurants) Replace(ctx context.Context, partner domain.Partner) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE platform.restaurants
		SET name = $2, base_url = $3, is_active = $4
		WHERE id = $1`,
		partner.ID, partner.Name, partner.BaseURL, partner.IsActive)
	if err != nil {
		return fmt.Errorf("изменить реквизиты заведения %s: %w", partner.ID, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("изменить реквизиты заведения %s: %w", partner.ID, err)
	}
	if changed == 0 {
		return domain.ErrRestaurantNotFound
	}

	return nil
}

// FindByKeyHash находит подключённое заведение по хешу его ключа.
//
// Отбор по `is_active` стоит в самом запросе: отключённое заведение перестаёт быть
// партнёром площадки целиком, а не только исчезает с витрины, — иначе его ключ продолжал
// бы открывать партнёрские операции после отключения.
func (r *Restaurants) FindByKeyHash(ctx context.Context, keyHash string) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, is_active
		FROM platform.restaurants
		WHERE api_key_hash = $1 AND is_active`, keyHash)

	var partner domain.Partner
	switch err := row.Scan(&partner.ID, &partner.Name, &partner.BaseURL, &partner.IsActive); {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Partner{}, domain.ErrRestaurantNotFound
	case err != nil:
		// Хеш в текст ошибки не попадает: сам по себе он поверхность не открывает, но
		// это единственное, что стоит между утёкшим логом и подбором ключа офлайн.
		return domain.Partner{}, fmt.Errorf("найти заведение по ключу: %w", err)
	}

	return partner, nil
}

// ListActive перечисляет подключённые заведения вместе с их реквизитами интеграции.
//
// Порядок задан явно: обход синхронизации идёт по нему, и заведение, оказавшееся
// последним, должно оставаться последним между прогонами — иначе одно и то же заведение
// то и дело оказывалось бы тем, до которого обход не дошёл.
func (r *Restaurants) ListActive(ctx context.Context) ([]domain.Partner, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, base_url, is_active
		FROM platform.restaurants
		WHERE is_active
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("прочитать подключённые заведения: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var partners []domain.Partner
	for rows.Next() {
		var partner domain.Partner
		if err := rows.Scan(&partner.ID, &partner.Name, &partner.BaseURL, &partner.IsActive); err != nil {
			return nil, fmt.Errorf("прочитать подключённое заведение: %w", err)
		}
		partners = append(partners, partner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перебрать подключённые заведения: %w", err)
	}

	return partners, nil
}

// Find отдаёт подключённое заведение по идентификатору.
//
// Отбор по `is_active` стоит в самом запросе по той же причине, по которой он стоит в
// поиске по ключу: отключённое заведение перестаёт быть партнёром площадки целиком —
// заказы ему не передаются, а не только карточка пропадает с витрины.
func (r *Restaurants) Find(ctx context.Context, id uuid.UUID) (domain.Partner, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, is_active
		FROM platform.restaurants
		WHERE id = $1 AND is_active`, id)

	var partner domain.Partner
	switch err := row.Scan(&partner.ID, &partner.Name, &partner.BaseURL, &partner.IsActive); {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Partner{}, domain.ErrRestaurantNotFound
	case err != nil:
		return domain.Partner{}, fmt.Errorf("прочитать заведение %s: %w", id, err)
	}

	return partner, nil
}
