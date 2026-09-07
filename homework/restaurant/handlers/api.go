package restaurant

import (
	"net/http"
	"sync"
)

type API struct {
	mu     sync.RWMutex
	orders map[int64]Order
}

func Init() {
	env := &API{orders: make(map[int64]Order)}

	//создать заказ
	http.HandleFunc("/restaurant/orders", env.MakeOrder)
	//отменить заказ
	http.HandleFunc("/restaurant/orders/{id}", env.CancelOrder)
	//узнать статус заказа
	http.HandleFunc("/restaurant/orders/{id}s", env.GetOrder)
}
