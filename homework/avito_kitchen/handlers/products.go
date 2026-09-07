package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	domain "myproject/avito_kitchen/domain"
)

var (
	ErrMethodNotPost   = errors.New("method not allowed: must be POST")
	ErrMethodNotDelete = errors.New("method not allowed: must be DELETE")
	ErrMethodNotPut    = errors.New("method not allowed: must be PUT")
	ErrMethodNotGet    = errors.New("method not allowed: must be GET")
	ErrWrongTitle      = errors.New("wrong title")
	ErrWrongStatus     = errors.New("ты зашел слишком далеко")
)

//получить все рестораны

func (e *TaskApi) getRestaurants(w http.ResponseWriter, r *http.Request) { //
	//проверяем метод запроса
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}
	limit := 2
	offset := 2

	ctx := r.Context()

	result, err := e.Store.GetRestaurants(ctx, limit, offset) //все правильно
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}
	//Сериализация и все дела:
	writeJson(w, http.StatusOK, result)

	//отправляем сборник данных о всех ресторанах
}

//получить конкретный ресторан

func (e *TaskApi) getRestaurant(w http.ResponseWriter, r *http.Request) { //
	//проверяем метод запроса
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotPost)
		return
	}
	restorauntID := r.URL.Query().Get("id")

	ctx := r.Context()

	numId, err := strconv.Atoi(restorauntID)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
	}

	result, err := e.Store.GetRestaurant(ctx, numId)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	//Сериализация и все дела:
	writeJson(w, http.StatusOK, result)
}

//получить все продукты одного ресторана

func (e *TaskApi) getRestaurantProducts(w http.ResponseWriter, r *http.Request) { //
	//проверяем метод запроса
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotPost)
		return
	}
	id := r.URL.Query().Get("id")
	limit := 2
	offset := 2

	ctx := r.Context()

	numId, err := strconv.Atoi(id)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	result, err := e.Store.GetRestaurantProducts(ctx, numId, limit, offset)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	//отправляем сборник данных о всех продуктах ресторана

	//Сериализация и все дела:
	writeJson(w, http.StatusOK, result)
}

//получить конретный продукт по id

func (e *TaskApi) getProductById(w http.ResponseWriter, r *http.Request) { //
	//проверяем метод запроса
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}
	id := r.URL.Query().Get("id")

	ctx := r.Context()
	numId, err := strconv.Atoi(id)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	result, err := e.Store.GetProductById(ctx, numId)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	//отправляем сборник данных об одном продукте

	//Сериализация и все дела:
	writeJson(w, http.StatusOK, result)
}

//получить конкретный продукт по name

func (e *TaskApi) getProductByName(w http.ResponseWriter, r *http.Request) { //
	//проверяем метод запроса
	if r.Method != http.MethodGet {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotPost)
		return
	}
	name := r.URL.Query().Get("name")

	ctx := r.Context()

	result, err := e.Store.GetProductByName(ctx, name)
	if err != nil {
		writeJson(w, http.StatusInternalServerError, ErrMethodNotGet)
		return
	}

	writeJson(w, http.StatusOK, result)
}

func writeJson(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")

	var serverAnswer any
	switch v := data.(type) {
	case error:
		serverAnswer = struct {
			Error string `json:"error"`
		}{
			Error: v.Error(),
		}
	case string:
		serverAnswer = struct {
			ID string `json:"id"`
		}{
			ID: v,
		}
	//все рестораны
	case []domain.Restaurant:
		if v == nil {
			v = []domain.Restaurant{}
		}
		serverAnswer = map[string]any{
			"restaurant": v,
		}
	//один ресторан
	case domain.Restaurant:
		serverAnswer = v
	//все товары
	case []domain.Product:
		if v == nil {
			v = []domain.Product{}
		}
		serverAnswer = map[string]any{
			"product": v,
		}
	//один товар
	case domain.Product: //есть
		serverAnswer = v
	default:
		w.WriteHeader(http.StatusInternalServerError)
		err := json.NewEncoder(w).Encode(map[string]string{"error": "Unsupported data type"})
		if err != nil {
			log.Printf("encode response: %v", err)
			return
		}
	}

	jsonData, err := json.Marshal(serverAnswer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, err = w.Write(jsonData)
	if err != nil {
		log.Printf("write response: %v", err)
	}
}
