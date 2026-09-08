package http

import (
	"fmt"
	"net/http"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/operator_v1"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// ListOrders отдаёт страницу входящих заказов: состав, адрес и состояние каждого.
//
// Разобранные заказы из списка не пропадают: оператору нужно видеть, что он подтвердил и
// от чего отказался, а не только то, что ждёт решения.
func (a operatorAPI) ListOrders(w http.ResponseWriter, r *http.Request, params operator_v1.ListOrdersParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, operatorDefaultPageSize, operatorMaxPageSize)
	if err != nil {
		writeOperatorProblem(w, r, http.StatusBadRequest, operator_v1.ProblemCodeValidationFailed,
			"Некорректные параметры запроса", err.Error())

		return
	}

	page, err := a.orders.Page(r.Context(), application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	orders := make([]operator_v1.Order, 0, len(page.Items))
	for _, order := range page.Items {
		shown, err := operatorOrder(order)
		if err != nil {
			a.refuse(w, r, err)

			return
		}

		orders = append(orders, shown)
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, operator_v1.OrderPage{
		Items: orders,
		Page: operator_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
		},
	})
}

// ConfirmOrder — оператор берётся за заказ.
func (a operatorAPI) ConfirmOrder(w http.ResponseWriter, r *http.Request, orderID operator_v1.OrderId) {
	order, err := a.orders.Confirm(r.Context(), orderID)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	a.writeOrder(w, r, order)
}

// RejectOrder — заведение не берётся за заказ и объясняет причину.
func (a operatorAPI) RejectOrder(w http.ResponseWriter, r *http.Request, orderID operator_v1.OrderId) {
	var body operator_v1.Rejection
	if err := decodeBody(r, &body, "reason"); err != nil {
		a.refuse(w, r, err)

		return
	}

	order, err := a.orders.Reject(r.Context(), orderID, body.Reason)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	a.writeOrder(w, r, order)
}

// writeOrder отдаёт оператору заказ целиком: после решения он видит его новое состояние,
// а не только код ответа.
func (a operatorAPI) writeOrder(w http.ResponseWriter, r *http.Request, order domain.Order) {
	shown, err := operatorOrder(order)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, shown)
}

// operatorOrder переводит заказ в схему рабочего места.
func operatorOrder(order domain.Order) (operator_v1.Order, error) {
	status, known := operatorStatus(order.Status)
	if !known {
		return operator_v1.Order{}, fmt.Errorf("состояние заказа %s (%q) не описано поверхностью оператора",
			order.ID, order.Status)
	}

	items := make([]operator_v1.OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, operator_v1.OrderItem{
			MenuItemId: item.MenuItemID,
			Name:       item.Name,
			Price:      int64(item.Price),
			Quantity:   item.Quantity,
		})
	}

	shown := operator_v1.Order{
		Id:              order.ID,
		PlatformOrderId: order.PlatformOrderID,
		Status:          status,
		Address:         order.Address,
		Items:           items,
		Total:           int64(order.Total),
		AcceptedAt:      order.AcceptedAt,
	}

	// Причины отказа и времени оформления может не быть — тогда полей нет и в ответе:
	// пустая строка сообщила бы, что заказ отклонили без объяснения, а нулевое время —
	// что покупатель оформил заказ в первую секунду эпохи.
	if order.CancellationReason != "" {
		shown.CancellationReason = &order.CancellationReason
	}
	if !order.PlacedAt.IsZero() {
		shown.PlacedAt = &order.PlacedAt
	}

	return shown, nil
}

// operatorStatus переводит состояние заказа в значение поверхности оператора.
//
// Перевод явный, а не приведением строки: перечень состояний в спецификации закрыт, и
// состояние, которого она не знает, — это расхождение коробки с собственным контрактом.
// Показать его оператору значило бы отдать документ, который его же клиент не разберёт.
func operatorStatus(status domain.OrderStatus) (operator_v1.OrderStatus, bool) {
	switch status {
	case domain.UnderReview:
		return operator_v1.UnderReview, true
	case domain.Confirmed:
		return operator_v1.Confirmed, true
	case domain.Cancelled:
		return operator_v1.Cancelled, true
	default:
		return "", false
	}
}
