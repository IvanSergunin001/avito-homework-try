package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// menuItemColumns — поля позиции в том порядке, в каком их читает scanMenuItem.
const menuItemColumns = `id, name, description, price, stock, in_stop_list`

// Menu — меню коробки в схеме `restaurant`.
type Menu struct {
	db *sql.DB
}

// NewMenu собирает хранилище меню поверх подключения к базе коробки.
func NewMenu(db *sql.DB) *Menu {
	return &Menu{db: db}
}

// Add заводит новую позицию меню.
func (m *Menu) Add(ctx context.Context, item domain.MenuItem) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO restaurant.menu_items (`+menuItemColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		item.ID, item.Name, item.Description, item.Price, item.Stock, item.InStopList)
	if err != nil {
		return fmt.Errorf("завести позицию меню: %w", err)
	}

	return nil
}

// Page отдаёт окно меню и общее число позиций.
//
// Порядок задан явно и включает идентификатор: без второго ключа одноимённые позиции
// переставлялись бы между запросами, и одна и та же попадала бы то на границу страницы
// дважды, то никуда — площадка, забирающая меню страницами, потеряла бы её из каталога.
func (m *Menu) Page(ctx context.Context, window application.Window) (application.MenuPage, error) {
	// Общее число считается отдельным запросом, а не оконной функцией поверх страницы:
	// страница за концом выдачи не содержит ни одной строки, и вместе со строками
	// потерялся бы и счётчик — читающий увидел бы «всего 0» там, где позиции есть.
	var total int64
	countRow := m.db.QueryRowContext(ctx, `SELECT count(*) FROM restaurant.menu_items`)
	if err := countRow.Scan(&total); err != nil {
		return application.MenuPage{}, fmt.Errorf("сосчитать позиции меню: %w", err)
	}

	rows, err := m.db.QueryContext(ctx, `
		SELECT `+menuItemColumns+`
		FROM restaurant.menu_items
		ORDER BY name, id
		LIMIT $1 OFFSET $2`, window.Limit, window.Offset)
	if err != nil {
		return application.MenuPage{}, fmt.Errorf("прочитать позиции меню: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Пустая страница — это пустой список, а не отсутствие списка: контракт обещает
	// коллекцию всегда, и различать два вида пустоты читающему незачем.
	items := make([]domain.MenuItem, 0, window.Limit)
	for rows.Next() {
		item, err := scanMenuItem(rows)
		if err != nil {
			return application.MenuPage{}, fmt.Errorf("прочитать позицию меню: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return application.MenuPage{}, fmt.Errorf("перебрать позиции меню: %w", err)
	}

	return application.MenuPage{Items: items, Total: total}, nil
}

// UpdateCard заменяет карточку позиции и возвращает позицию целиком.
func (m *Menu) UpdateCard(
	ctx context.Context, id uuid.UUID, card domain.MenuCard,
) (domain.MenuItem, error) {
	return m.update(ctx, `
		UPDATE restaurant.menu_items
		SET name = $2, description = $3, price = $4
		WHERE id = $1
		RETURNING `+menuItemColumns,
		id, card.Name, card.Description, card.Price)
}

// SetStopListMark выставляет или снимает отметку стоп-листа.
func (m *Menu) SetStopListMark(ctx context.Context, id uuid.UUID, inStopList bool) (domain.MenuItem, error) {
	return m.update(ctx, `
		UPDATE restaurant.menu_items
		SET in_stop_list = $2
		WHERE id = $1
		RETURNING `+menuItemColumns,
		id, inStopList)
}

// SetStock записывает остаток позиции.
func (m *Menu) SetStock(ctx context.Context, id uuid.UUID, stock domain.Stock) (domain.MenuItem, error) {
	return m.update(ctx, `
		UPDATE restaurant.menu_items
		SET stock = $2
		WHERE id = $1
		RETURNING `+menuItemColumns,
		id, stock)
}

// Remove убирает позицию из меню.
func (m *Menu) Remove(ctx context.Context, id uuid.UUID) error {
	result, err := m.db.ExecContext(ctx, `DELETE FROM restaurant.menu_items WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("удалить позицию меню %s: %w", id, err)
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("удалить позицию меню %s: %w", id, err)
	}
	if removed == 0 {
		return domain.ErrMenuItemNotFound
	}

	return nil
}

// update выполняет правку позиции и читает результат.
//
// Правка возвращает позицию тем же запросом (`RETURNING`), а не вторым чтением: между
// правкой и чтением оператор с соседнего рабочего места успевает поменять остаток, и
// первый увидел бы ответ на чужое действие вместо последствия своего. Отсутствие строки
// в результате и означает, что позиции нет: проверять её существование отдельным запросом
// значило бы гадать между двумя запросами.
func (m *Menu) update(ctx context.Context, query string, args ...any) (domain.MenuItem, error) {
	item, err := scanMenuItem(m.db.QueryRowContext(ctx, query, args...))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.MenuItem{}, domain.ErrMenuItemNotFound
	case err != nil:
		return domain.MenuItem{}, fmt.Errorf("изменить позицию меню: %w", err)
	}

	return item, nil
}

// rowScanner — строка результата: годятся и одиночная строка, и очередная строка выдачи.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMenuItem читает позицию меню из строки результата.
func scanMenuItem(row rowScanner) (domain.MenuItem, error) {
	var item domain.MenuItem
	err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Price, &item.Stock, &item.InStopList)
	if err != nil {
		return domain.MenuItem{}, err
	}

	return item, nil
}
