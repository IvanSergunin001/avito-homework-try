package domain

type CreateOrderRequest struct {
	ExternalOrderID int64       `json:"external_order_id"`
	Address         string      `json:"address"`
	Items           []OrderItem `json:"items"`
}

type OrderItem struct {
	ExternalProductID string `json:"external_product_id"`
	Quantity          int    `json:"quantity"`
}

type Order struct {
	ExternalOrderID int64       `json:"external_order_id"`
	Address         string      `json:"address"`
	Status          string      `json:"status"`
	Items           []OrderItem `json:"items"`
}
