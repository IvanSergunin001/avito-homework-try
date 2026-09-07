package handlers

import (
	repo "myproject/avito_kitchen/database/repo"
	"net/http"
)

type TaskApi struct {
	Store *repo.TaskStoragePostgres
}

func Init(store *repo.TaskStoragePostgres) {
	env := &TaskApi{Store: store}

	//получить все рестораны
	http.HandleFunc("/restaurants", env.getRestaurants)
	//получить конкретный ресторан
	http.HandleFunc("/restaurants/restaurant_id", env.getRestaurant)
	//получить все продукты одного ресторана
	http.HandleFunc("/restaurants/restaurant_id/products", env.getRestaurantProducts)
	//получить конретный продукт по id
	http.HandleFunc("/products/product_id", env.getProductById)
	//получить конкретный продукт по name
	http.HandleFunc("/products/product_name", env.getProductByName)

	//создать заказ
	http.HandleFunc("/products/product_name", env.makeOrder)
	//отменить заказ
	http.HandleFunc("/products/product_name", env.cancelOrder)
	//узнать статус заказа
	http.HandleFunc("/restaurant/orders", env.getOrder)
}
