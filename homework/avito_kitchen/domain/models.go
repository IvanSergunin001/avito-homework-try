package domain

type Product struct {
	ID           int64
	RestaurantID int64
	Name         string
	Description  string
	PriceMinor   int64
	IsActive     bool
}

// Ресторан
type Restaurant struct {
	ID   int64
	Name string
}

// Запрос клиента на создание заказа
type CreateOrderRequest struct {
	RestaurantID int64             `json:"restaurant_id"`
	Address      string            `json:"address"`
	Items        []CreateOrderItem `json:"items"`
}

// Один товар в запросе клиента
type CreateOrderItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type Order struct {
	ID           int64
	RestaurantID int64
	Status       string
	TotalMinor   int64
	Address      string
}

type OrderItem struct {
	ID         int64
	OrderID    int64
	ProductID  int64
	PriceMinor int64
	Quantity   int
}

type RestaurantOrderResponse struct {
	ExternalOrderID int64  `json:"external_order_id"`
	Status          string `json:"status"`
}

type OrderResp struct {
	ID           int64       `json:"id"`
	RestaurantID int64       `json:"restaurant_id"`
	Status       string      `json:"status"`
	TotalMinor   int64       `json:"total_minor"`
	Address      string      `json:"address"`
	Items        []OrderItem `json:"items"`
}
