package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domain "myproject/avito_kitchen/domain"
)

type TaskStoragePostgres struct {
	db *sql.DB
}

func NewCustomerRepository(db *sql.DB) *TaskStoragePostgres {
	return &TaskStoragePostgres{db: db}
}

//МЕНЮ

//получить все рестораны

func (s *TaskStoragePostgres) GetRestaurants(ctx context.Context, limit, offset int) ([]domain.Restaurant, error) { //
	var restaurants []domain.Restaurant

	rows, err := s.db.QueryContext(ctx, "SELECT id, name FROM restaurants LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return restaurants, err
	}
	defer rows.Close()

	for rows.Next() {
		restaurant := domain.Restaurant{}
		err := rows.Scan(&restaurant.ID, &restaurant.Name)
		if err != nil {
			return restaurants, err
		}
		restaurants = append(restaurants, restaurant)

	}
	if err := rows.Err(); err != nil {
		return restaurants, err
	}
	return restaurants, nil
}

// получить один ресторан
func (s *TaskStoragePostgres) GetRestaurant(ctx context.Context, restaurantID int) (domain.Restaurant, error) { //
	var restaurant domain.Restaurant

	err := s.db.QueryRowContext(ctx, `SELECT name FROM restaurants WHERE id = $1`, restaurantID).Scan(&restaurant.Name)
	if err != nil {
		return domain.Restaurant{}, err
	}

	return restaurant, nil
}

// получить все продукты одного ресторана

func (s *TaskStoragePostgres) GetRestaurantProducts(ctx context.Context, restaurantID, limit, offset int) ([]domain.Product, error) {
	var products []domain.Product

	rows, err := s.db.QueryContext(ctx, "SELECT id, name, description, price_minor, is_active FROM product WHERE restoraunt_id = $1 LIMIT $2 OFFSET $3", restaurantID, limit, offset)
	if err != nil {
		return products, err
	}
	defer rows.Close()

	for rows.Next() {
		product := domain.Product{}
		err := rows.Scan(&product.ID, &product.Name, &product.Description, &product.PriceMinor, &product.IsActive)
		if err != nil {
			return products, err
		}
		products = append(products, product)

	}
	if err := rows.Err(); err != nil {
		return products, err
	}
	return products, nil
}

// получить продукт по id

func (s *TaskStoragePostgres) GetProductById(ctx context.Context, id int) (domain.Product, error) {
	var product domain.Product

	row := s.db.QueryRowContext(ctx, "SELECT restaurant_id, name, description, price_minor, is_active FROM products WHERE id = $id", id)
	err := row.Scan(&product.RestaurantID, &product.Name, &product.Description, &product.PriceMinor, &product.IsActive)
	if err != nil {
		fmt.Println(err)
		return product, err
	}
	return product, nil
}

// получить продукт по name

func (s *TaskStoragePostgres) GetProductByName(ctx context.Context, name string) (domain.Product, error) {
	var product domain.Product

	row := s.db.QueryRowContext(ctx, "SELECT id, restaurant_id, description, price_minor, is_active FROM products WHERE name = $name", name)
	err := row.Scan(&product.ID, &product.RestaurantID, &product.Description, &product.PriceMinor, &product.IsActive)
	if err != nil {
		fmt.Println(err)
		return product, err
	}
	return product, nil
}

//ЗАКАЗЫ

//Получить заказ

func (s *TaskStoragePostgres) GetOrder(ctx context.Context, id int) (domain.OrderResp, error) {
	var order domain.Order

	row := s.db.QueryRowContext(ctx, "SELECT id, restaurant_id, status, total_minor, address FROM orders WHERE id = $id", id)
	err := row.Scan(&order.ID, &order.RestaurantID, &order.Status, &order.TotalMinor, &order.Address)
	if err != nil {
		fmt.Println(err)
		return domain.OrderResp{}, err
	}

	var order_items []domain.OrderItem

	rows, err := s.db.QueryContext(ctx, "SELECT id, order_id, product_id, price_minor, quantity FROM product WHERE order_id = $1", id)
	if err != nil {
		return domain.OrderResp{}, err
	}
	defer rows.Close()

	for rows.Next() {
		item := domain.OrderItem{}
		err := rows.Scan(&item.ID, &item.OrderID, &item.ProductID, &item.PriceMinor, &item.Quantity)
		if err != nil {
			return domain.OrderResp{}, err
		}
		order_items = append(order_items, item)

	}
	if err := rows.Err(); err != nil {
		return domain.OrderResp{}, err
	}

	orderResp := domain.OrderResp{
		ID:           order.ID,
		RestaurantID: order.RestaurantID,
		Status:       order.Status,
		TotalMinor:   order.TotalMinor,
		Address:      order.Address,
		Items:        order_items,
	}
	return orderResp, nil
}

func (s *TaskStoragePostgres) GetOrderStatus(ctx context.Context, id int) (string, error) {
	var status string

	err := s.db.QueryRowContext(ctx, `SELECT name FROM restaurants WHERE id = $1`, id).Scan(&status)
	if err != nil {
		return "", err
	}

	return status, nil
}

//Создать заказ

//Удалить заказ

func (s *TaskStoragePostgres) CancelOrder(ctx context.Context, id int, status string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE order SET status = $1 WHERE id = $2", status, id)

	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("incorrect id for updating task")
	}
	return nil
}

func (e *TaskStoragePostgres) CreateOrderInDB(ctx context.Context, req domain.CreateOrderRequest) (int64, int64, []domain.OrderItem, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, nil, err
	}

	defer tx.Rollback()

	var orderID int64

	err = tx.QueryRowContext(
		ctx, `INSERT INTO orders (restaurant_id, status, total_minor, address)VALUES ($1, $2, $3, $4)RETURNING id`, req.RestaurantID, "created", 0, req.Address).Scan(&orderID)

	if err != nil {
		return 0, 0, nil, err
	}

	var totalMinor int64

	orderItems := make([]domain.OrderItem, 0, len(req.Items))

	for _, item := range req.Items {
		if item.Quantity <= 0 {
			return 0, 0, nil, fmt.Errorf("invalid quantity")
		}

		var priceMinor int64
		var restaurantID int64
		var isActive bool

		err = tx.QueryRowContext(ctx, `SELECT price_minor, restaurant_id, is_activeFROM productsWHERE id = $1`, item.ProductID).Scan(&priceMinor, &restaurantID, &isActive)

		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil, fmt.Errorf("product not found")
		}

		if err != nil {
			return 0, 0, nil, err
		}

		if restaurantID != req.RestaurantID {
			return 0, 0, nil,
				fmt.Errorf("product belongs to another restaurant")
		}

		if !isActive {
			return 0, 0, nil,
				fmt.Errorf("product is not available")
		}

		_, err = tx.ExecContext(ctx, `INSERT INTO order_items(order_id, product_id, price_minor, quantity)VALUES ($1, $2, $3, $4)`, orderID, item.ProductID, priceMinor, item.Quantity)

		if err != nil {
			return 0, 0, nil, err
		}

		totalMinor += priceMinor * int64(item.Quantity)

		orderItems = append(orderItems, domain.OrderItem{
			ProductID:  item.ProductID,
			PriceMinor: priceMinor,
			Quantity:   item.Quantity,
		})
	}

	_, err = tx.ExecContext(ctx, `UPDATE ordersSET total_minor = $1WHERE id = $2`, totalMinor, orderID)

	if err != nil {
		return 0, 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, nil, err
	}

	return orderID, totalMinor, orderItems, nil
}
