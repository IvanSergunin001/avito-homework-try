package restaurant

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (a *API) MakeOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.ExternalOrderID <= 0 {
		http.Error(w, "invalid order id", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "order must contain items", http.StatusBadRequest)
		return
	}

	for _, item := range req.Items {
		if item.ExternalProductID == "" {
			http.Error(w, "invalid product id", http.StatusBadRequest)
			return
		}

		if item.Quantity <= 0 {
			http.Error(w, "quantity must be greater than zero", http.StatusBadRequest)
			return
		}
	}

	order := Order{
		ExternalOrderID: req.ExternalOrderID,
		Address:         req.Address,
		Status:          "preparing",
		Items:           req.Items,
	}

	a.mu.Lock()
	a.orders[order.ExternalOrderID] = order
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	json.NewEncoder(w).Encode(order)
}

func (a *API) GetOrder(w http.ResponseWriter, r *http.Request) {
	idString := r.PathValue("id")

	id, err := strconv.ParseInt(idString, 10, 64)
	if err != nil {
		http.Error(w, "invalid order id", http.StatusBadRequest)
		return
	}

	a.mu.RLock()
	order, ok := a.orders[id]
	a.mu.RUnlock()

	if !ok {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(order); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *API) CancelOrder(w http.ResponseWriter, r *http.Request) {
	idString := r.PathValue("id")

	id, err := strconv.ParseInt(idString, 10, 64)
	if err != nil {
		http.Error(w, "invalid order id", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	order, ok := a.orders[id]
	if !ok {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}

	order.Status = "cancelled"

	a.orders[id] = order

	w.WriteHeader(http.StatusNoContent)
}
