package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// orderColumns — поля заказа в том порядке, в каком их читает scanOrder.
const orderColumns = `id, customer_id, restaurant_id, status, address, items, total,
	cancelled_by, cancellation_reason, created_at, updated_at`

// Orders — заказы покупателей в схеме `platform`.
type Orders struct {
	db *sql.DB
}

// NewOrders собирает хранилище заказов поверх подключения к базе площадки.
func NewOrders(db *sql.DB) *Orders {
	return &Orders{db: db}
}

// Add записывает оформленный заказ.
func (o *Orders) Add(ctx context.Context, order domain.Order) error {
	items, err := json.Marshal(storedItems(order.Items))
	if err != nil {
		return fmt.Errorf("собрать состав заказа %s: %w", order.ID, err)
	}

	_, err = o.db.ExecContext(ctx, `
		INSERT INTO platform.orders
			(id, customer_id, restaurant_id, status, address, items, total,
			 cancelled_by, cancellation_reason, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		order.ID, order.CustomerID, order.RestaurantID, string(order.Status), order.Address,
		items, int64(order.Total), string(order.CancelledBy), order.CancellationReason,
		order.CreatedAt, order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("записать заказ %s: %w", order.ID, err)
	}

	return nil
}

// SaveStatus записывает новое состояние заказа.
//
// В списке колонок нет ни состава, ни суммы, ни адреса, и это не экономия запроса:
// переход состояния не вправе тронуть то, что покупатель уже оформил, а запрос, который
// перечисляет все колонки «на всякий случай», однажды перепишет заказ задним числом.
func (o *Orders) SaveStatus(ctx context.Context, order domain.Order) error {
	result, err := o.db.ExecContext(ctx, `
		UPDATE platform.orders
		SET status = $2, cancelled_by = $3, cancellation_reason = $4, updated_at = $5
		WHERE id = $1`,
		order.ID, string(order.Status), string(order.CancelledBy),
		order.CancellationReason, order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("записать состояние заказа %s: %w", order.ID, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("записать состояние заказа %s: %w", order.ID, err)
	}
	if changed == 0 {
		return domain.ErrOrderNotFound
	}

	return nil
}

// FindForCustomer отдаёт заказ, принадлежащий этому покупателю.
//
// Принадлежность стоит условием самого запроса, а не проверкой после чтения: заказ,
// прочитанный «чтобы посмотреть, чей он», уже лежит в памяти службы, и забытая проверка
// отдаёт его целиком. Чужой заказ здесь неотличим от несуществующего — этого заказа у
// покупателя нет ни в том, ни в другом случае.
func (o *Orders) FindForCustomer(ctx context.Context, id, customer uuid.UUID) (domain.Order, error) {
	row := o.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM platform.orders WHERE id = $1 AND customer_id = $2`, id, customer)

	order, err := scanOrder(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	return order, nil
}

// storedItem — позиция заказа в том виде, в каком она лежит в базе.
//
// Отдельный тип, а не сам domain.OrderItem: формат хранения не должен меняться от того,
// что кто-то переименовал поле в предметной области, — прочитать после этого уже
// записанные заказы было бы нечем.
type storedItem struct {
	ProductID  uuid.UUID `json:"product_id"`
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	Price      int64     `json:"price"`
	Quantity   int       `json:"quantity"`
}

// storedItems переводит состав заказа в формат хранения.
func storedItems(items []domain.OrderItem) []storedItem {
	stored := make([]storedItem, 0, len(items))
	for _, item := range items {
		stored = append(stored, storedItem{
			ProductID:  item.ProductID,
			ExternalID: item.ExternalID,
			Name:       item.Name,
			Price:      int64(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return stored
}

// scanOrder читает заказ из строки результата.
func scanOrder(row rowScanner) (domain.Order, error) {
	var (
		order       domain.Order
		status      string
		cancelledBy string
		items       []byte
		total       int64
	)

	err := row.Scan(&order.ID, &order.CustomerID, &order.RestaurantID, &status, &order.Address,
		&items, &total, &cancelledBy, &order.CancellationReason, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return domain.Order{}, err
	}

	var stored []storedItem
	if err := json.Unmarshal(items, &stored); err != nil {
		return domain.Order{}, fmt.Errorf("разобрать состав заказа %s: %w", order.ID, err)
	}

	order.Status = domain.OrderStatus(status)
	order.CancelledBy = domain.Actor(cancelledBy)
	order.Total = domain.Money(total)
	order.Items = make([]domain.OrderItem, 0, len(stored))
	for _, item := range stored {
		order.Items = append(order.Items, domain.OrderItem{
			ProductID:  item.ProductID,
			ExternalID: item.ExternalID,
			Name:       item.Name,
			Price:      domain.Money(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return order, nil
}
