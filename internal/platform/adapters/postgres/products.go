package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// productColumns — поля товара в том порядке, в каком их читает scanProduct.
const productColumns = `p.id, p.restaurant_id, p.name, p.description, p.price, p.is_available`

// fromWorkingRestaurants — источник выдачи витрины: товары вместе с их заведениями.
// Соединение с заведениями не украшение запроса, а его условие: витрина показывает
// только то, что можно заказать, а заказать можно только в работающем заведении.
const fromWorkingRestaurants = `
	FROM platform.products p
	JOIN platform.restaurants r ON r.id = p.restaurant_id`

// Products — товары площадки в схеме `platform`.
type Products struct {
	db *sql.DB
}

// NewProducts собирает хранилище товаров поверх подключения к базе площадки.
func NewProducts(db *sql.DB) *Products {
	return &Products{db: db}
}

// Search отдаёт окно товаров, подошедших под запрос, и общее их число.
//
// Порядок задан явно и включает идентификатор: без второго ключа одноимённые позиции
// переставлялись бы между запросами, и одна и та же попадала бы то на границу страницы
// дважды, то никуда.
func (p *Products) Search(ctx context.Context, query application.ProductQuery) (application.ProductPage, error) {
	where, args := searchFilter(query)

	// Общее число считается отдельным запросом, а не оконной функцией поверх страницы:
	// страница за концом выдачи не содержит ни одной строки, и вместе со строками
	// потерялся бы и счётчик — клиент увидел бы «всего 0» там, где записи есть.
	// Подавление точечное и с причиной: gosec видит склейку текста запроса и не умеет
	// отличить её от склейки со значением, пришедшим от клиента. Склеиваются только
	// константы этого файла и условие, собранное searchFilter из них же; всё, что пришло
	// снаружи, уходит параметрами.
	var total int64
	countRow := p.db.QueryRowContext(ctx, `SELECT count(*)`+fromWorkingRestaurants+where, args...) //nolint:gosec // склеиваются только константы, значения уходят параметрами
	if err := countRow.Scan(&total); err != nil {
		return application.ProductPage{}, fmt.Errorf("сосчитать товары витрины: %w", err)
	}

	window := fmt.Sprintf(" ORDER BY p.name, p.id LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+productColumns+fromWorkingRestaurants+where+window, //nolint:gosec // склеиваются только константы, значения уходят параметрами
		append(args, query.Page.Limit, query.Page.Offset)...)
	if err != nil {
		return application.ProductPage{}, fmt.Errorf("прочитать товары витрины: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Пустая страница — это пустой список, а не отсутствие списка: контракт обещает
	// коллекцию всегда, и различать два вида пустоты клиенту незачем.
	products := make([]domain.Product, 0, query.Page.Limit)
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return application.ProductPage{}, fmt.Errorf("прочитать товар витрины: %w", err)
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return application.ProductPage{}, fmt.Errorf("перебрать товары витрины: %w", err)
	}

	return application.ProductPage{Products: products, Total: total}, nil
}

// Find отдаёт товар работающего заведения по идентификатору.
//
// Условия «заведение работает» и «позиция есть в меню» стоят в самом запросе: карточка
// не должна открываться по прямой ссылке после того, как товар исчез из выдачи, — ни
// когда закрылось заведение, ни когда позиция пропала из его выгрузки.
func (p *Products) Find(ctx context.Context, id uuid.UUID) (domain.Product, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT `+productColumns+fromWorkingRestaurants+` WHERE p.id = $1 AND r.is_active AND p.is_active`, id)

	product, err := scanProduct(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Product{}, domain.ErrProductNotFound
	case err != nil:
		return domain.Product{}, fmt.Errorf("прочитать товар %s: %w", id, err)
	}

	return product, nil
}

// searchFilter собирает условие отбора и его аргументы.
//
// Условия попадают в текст запроса, а не сворачиваются в одно выражение вида
// `($1 IS NULL OR …)`: планировщик выбирает индекс по тому, что видит в запросе, и такое
// выражение лишает его этой возможности — поиск по названию перестал бы попадать в
// триграммный индекс, а меню заведения — в индекс меню. Значения при этом остаются
// параметрами: в текст запроса не попадает ничего, что пришло от клиента.
func searchFilter(query application.ProductQuery) (string, []any) {
	// Деактивированная позиция — та, которой не оказалось в последней выгрузке заведения.
	// Она остаётся в таблице ради ссылок и истории, но витрина её не показывает: заказать
	// то, чего у заведения больше нет, нельзя.
	conditions := []string{"r.is_active", "p.is_active"}
	args := make([]any, 0, 2)

	if query.Restaurant != nil {
		args = append(args, *query.Restaurant)
		conditions = append(conditions, fmt.Sprintf("p.restaurant_id = $%d", len(args)))
	}
	if query.NameContains != "" {
		args = append(args, "%"+escapeLikePattern(query.NameContains)+"%")
		conditions = append(conditions, fmt.Sprintf("p.name ILIKE $%d", len(args)))
	}

	return " WHERE " + strings.Join(conditions, " AND "), args
}

// escapeLikePattern обезвреживает то, что LIKE считает своим синтаксисом.
//
// Покупатель ищет подстроку, а не шаблон: набрав «50%», он ищет скидку, а не «всё, где
// есть 50». Без экранирования его знаки процента и подчёркивания превратились бы в
// подстановочные, и выдача перестала бы соответствовать запросу.
func escapeLikePattern(substring string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(substring)
}

// rowScanner — строка результата: годятся и одиночная строка, и очередная строка выдачи.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanProduct читает товар из строки результата.
func scanProduct(row rowScanner) (domain.Product, error) {
	var product domain.Product
	err := row.Scan(&product.ID, &product.RestaurantID, &product.Name,
		&product.Description, &product.Price, &product.IsAvailable)
	if err != nil {
		return domain.Product{}, err
	}

	return product, nil
}

// Positions отдаёт позиции каталога работающего заведения по идентификаторам товаров.
//
// Отбор тот же, что и у витрины: заказать можно только то, что покупатель видит, — товар
// исчезнувшей позиции или закрывшегося заведения в выдачу не попадает, и заказ на него не
// соберётся. Товара, которого нет, в выдаче просто нет: чем это обернётся для заказа,
// решает сценарий, а не запрос к базе.
func (p *Products) Positions(
	ctx context.Context, restaurant uuid.UUID, ids []uuid.UUID,
) ([]application.CatalogPosition, error) {
	wanted := make([]string, 0, len(ids))
	for _, id := range ids {
		wanted = append(wanted, id.String())
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT p.id, p.external_id, p.name, p.price`+fromWorkingRestaurants+`
		WHERE p.restaurant_id = $1 AND p.id = ANY($2::uuid[]) AND r.is_active AND p.is_active`,
		restaurant, wanted)
	if err != nil {
		return nil, fmt.Errorf("прочитать позиции заказа: %w", err)
	}
	defer func() { _ = rows.Close() }()

	positions := make([]application.CatalogPosition, 0, len(ids))
	for rows.Next() {
		var position application.CatalogPosition
		if err := rows.Scan(&position.ProductID, &position.ExternalID,
			&position.Name, &position.Price); err != nil {
			return nil, fmt.Errorf("прочитать позицию заказа: %w", err)
		}
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перебрать позиции заказа: %w", err)
	}

	return positions, nil
}
