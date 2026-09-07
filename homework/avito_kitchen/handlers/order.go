package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	domain "myproject/avito_kitchen/domain"
	"net/http"
	"strconv"
)

func (e *TaskApi) makeOrder(w http.ResponseWriter, r *http.Request) {
	// получаем данные от клиента

	ctx := r.Context()

	// 1. Получаем заказ от клиента
	var req domain.CreateOrderRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 2. Простая валидация
	if req.RestaurantID <= 0 {
		http.Error(w, "invalid restaurant id", http.StatusBadRequest)
		return
	}

	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "order must contain items", http.StatusBadRequest)
		return
	}

	// 3. Начинаем транзакцию

	// 8. Отправляем заказ ресторану
	restaurantOrder, err := makeOrder(req)

	if err != nil {
		http.Error(w, "restaurant is unavailable", http.StatusBadGateway)
		return
	}

	// 9. Обновляем статус после ответа ресторана
	orderID, totalMinor, orderItems, err := e.Store.CreateOrderInDB(ctx, req)
	if err != nil {
		http.Error(w, "failed to update order status", http.StatusInternalServerError)
		return
	}

	// 10. Формируем ответ клиенту
	order := domain.OrderResp{
		ID:           orderID,
		RestaurantID: req.RestaurantID,
		Status:       restaurantOrder.Status,
		TotalMinor:   totalMinor,
		Address:      req.Address,
		Items:        orderItems,
	}

	writeJson(w, http.StatusOK, order)
}

func (e *TaskApi) cancelOrder(w http.ResponseWriter, r *http.Request) {
	// получаем id заказа
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}
	id := r.URL.Query().Get("id")

	ctx := r.Context()

	numId, err := strconv.Atoi(id)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
	}

	// получаем заказ из своей БД
	status, err := e.Store.GetOrderStatus(ctx, numId)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
	}

	// проверяем, можно ли его отменить
	if status != "CONFIRMED" {
		writeJson(w, http.StatusInternalServerError, ErrWrongStatus)
	}
	// отправляем запрос на отмену ресторану
	err = cancelOrder(numId)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrWrongStatus)
	}

	// если ресторан подтвердил отмену:
	// меняем статус заказа в своей БД на cancelled
	err = e.Store.CancelOrder(ctx, numId, "CANCEL")

	// возвращаем успешный ответ
}

func (e *TaskApi) getOrder(w http.ResponseWriter, r *http.Request) {
	//получаем id заказа
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}
	id := r.URL.Query().Get("id")

	ctx := r.Context()

	numId, err := strconv.Atoi(id)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
	}

	result, err := e.Store.GetOrder(ctx, numId)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	writeJson(w, http.StatusOK, result)
}

func getOrderStatus(orderId int) (domain.RestaurantOrderResponse, error) {
	url := fmt.Sprintf("http://localhost:8080/orders/%d", orderId)

	client := &http.Client{}

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return domain.RestaurantOrderResponse{}, err
	}

	response, err := client.Do(request)
	if err != nil {
		return domain.RestaurantOrderResponse{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return domain.RestaurantOrderResponse{}, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	var order domain.RestaurantOrderResponse

	if err := json.NewDecoder(response.Body).Decode(&order); err != nil {
		return domain.RestaurantOrderResponse{}, err
	}

	return order, nil
}

func makeOrder(orderRequest domain.CreateOrderRequest) (domain.RestaurantOrderResponse, error) {
	jsonData, err := json.Marshal(orderRequest)
	if err != nil {
		return domain.RestaurantOrderResponse{}, err
	}

	client := &http.Client{}

	request, err := http.NewRequest(http.MethodPost, "http://localhost:8080/orders", bytes.NewBuffer(jsonData))
	if err != nil {
		return domain.RestaurantOrderResponse{}, err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return domain.RestaurantOrderResponse{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		return domain.RestaurantOrderResponse{}, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	var order domain.RestaurantOrderResponse

	if err := json.NewDecoder(response.Body).Decode(&order); err != nil {
		return domain.RestaurantOrderResponse{}, err
	}
	return order, nil
}

func cancelOrder(orderId int) error {
	url := fmt.Sprintf("http://localhost:8080/orders/%d", orderId)

	client := &http.Client{}

	request, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	return nil
}
