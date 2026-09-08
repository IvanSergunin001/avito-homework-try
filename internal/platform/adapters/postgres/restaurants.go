package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Restaurants — заведения площадки в схеме `platform`.
type Restaurants struct {
	db *sql.DB
}

// NewRestaurants собирает хранилище заведений поверх подключения к базе площадки.
func NewRestaurants(db *sql.DB) *Restaurants {
	return &Restaurants{db: db}
}

// ListWorking отдаёт окно списка работающих заведений и общее их число.
//
// Порядок задан явно и включает идентификатор: без второго ключа заведения с одинаковым
// названием переставлялись бы между запросами, и одно и то же заведение попадало бы
// то на границу страницы дважды, то никуда.
func (r *Restaurants) ListWorking(ctx context.Context, window application.Window) (application.RestaurantPage, error) {
	// Общее число считается отдельным запросом, а не оконной функцией поверх страницы:
	// страница за концом выдачи не содержит ни одной строки, и вместе со строками
	// потерялся бы и счётчик — клиент увидел бы «всего 0» там, где записи есть.
	var total int64
	countRow := r.db.QueryRowContext(ctx, `SELECT count(*) FROM platform.restaurants WHERE is_active`)
	if err := countRow.Scan(&total); err != nil {
		return application.RestaurantPage{}, fmt.Errorf("сосчитать работающие заведения: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name
		FROM platform.restaurants
		WHERE is_active
		ORDER BY name, id
		LIMIT $1 OFFSET $2`, window.Limit, window.Offset)
	if err != nil {
		return application.RestaurantPage{}, fmt.Errorf("прочитать работающие заведения: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Пустая страница — это пустой список, а не отсутствие списка: контракт обещает
	// коллекцию всегда, и различать два вида пустоты клиенту незачем.
	restaurants := make([]domain.Restaurant, 0, window.Limit)
	for rows.Next() {
		var restaurant domain.Restaurant
		if err := rows.Scan(&restaurant.ID, &restaurant.Name); err != nil {
			return application.RestaurantPage{}, fmt.Errorf("прочитать заведение: %w", err)
		}
		restaurants = append(restaurants, restaurant)
	}
	if err := rows.Err(); err != nil {
		return application.RestaurantPage{}, fmt.Errorf("перебрать работающие заведения: %w", err)
	}

	return application.RestaurantPage{Restaurants: restaurants, Total: total}, nil
}

// FindWorking отдаёт работающее заведение по идентификатору.
//
// Условие «работает» стоит в самом запросе: карточка неработающего заведения не должна
// открываться по прямой ссылке после того, как заведение исчезло из выдачи.
func (r *Restaurants) FindWorking(ctx context.Context, id uuid.UUID) (domain.Restaurant, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name
		FROM platform.restaurants
		WHERE id = $1 AND is_active`, id)

	var restaurant domain.Restaurant
	switch err := row.Scan(&restaurant.ID, &restaurant.Name); {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Restaurant{}, domain.ErrRestaurantNotFound
	case err != nil:
		return domain.Restaurant{}, fmt.Errorf("прочитать заведение %s: %w", id, err)
	}

	return restaurant, nil
}
