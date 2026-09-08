package http

import (
	"errors"
	"net/http"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/admin_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Отмена заказа через саппорта: единственная операция админской поверхности, которая
// касается заказов, а не заведений. Отдельным файлом от подключения заведений не по
// слоям, а по предмету: там администратор заводит партнёра, здесь сотрудник площадки
// разбирает обращение о конкретном заказе.

// CancelOrderBySupport отменяет заказ по обращению заведения, разобранному сотрудником
// площадки.
//
// Заведение приходит сюда тогда, когда отменить заказ само уже не может: после начала
// готовки продукты списаны, покупателю нужна компенсация, а безнаказанная отмена — повод
// для злоупотреблений. Поэтому причина обязательна: её увидит покупатель, и она же
// остаётся в журнале заказа вместе с инициатором `support` — по ней видно, что решение
// принимал человек площадки, а не кнопка на кухне.
//
// Заказ, который прожил свою жизнь, не отменяется и здесь — `409` с кодом
// `order_not_cancellable`: доставленный заказ отменять нечего, а отменённый уже отменён.
func (a adminAPI) CancelOrderBySupport(w http.ResponseWriter, r *http.Request, orderID admin_v1.OrderId) {
	var body admin_v1.SupportCancellation
	if err := decodeCancellationBody(r, &body); err != nil {
		a.refuseCancellation(w, r, err)

		return
	}

	order, err := a.cancellation.BySupport(r.Context(), orderID, body.Reason)
	if err != nil {
		a.refuseCancellation(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, adminOrderDocument(order))
}

// decodeCancellationBody разбирает тело отмены и переводит отказ разбора в ошибку
// предметной области заказа: причину читает сотрудник площадки, и отвечает ему та же
// ветка, что и на отмену без причины.
func decodeCancellationBody(r *http.Request, body *admin_v1.SupportCancellation) error {
	if err := decodeBody(r, body, "reason"); err != nil {
		return domain.InvalidOrderError{Reason: err.Error()}
	}

	return nil
}

// refuseCancellation отвечает на ошибку отмены заказа.
//
// Отказ матрицы переходов доезжает отдельным кодом причины: `order_not_cancellable` —
// это ответ «заказ в таком состоянии не отменяют», и по нему саппорт видит, что заказ
// уже доставлен или отменён раньше, а не что он неверно составил запрос.
func (a adminAPI) refuseCancellation(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid    domain.InvalidOrderError
		transition domain.OrderTransitionError
	)

	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		writeAdminProblem(w, r, http.StatusNotFound, admin_v1.ProblemCodeOrderNotFound,
			"Заказ не найден", "заказа с таким идентификатором на площадке нет")
	case errors.As(err, &transition):
		writeAdminProblem(w, r, http.StatusConflict, admin_v1.ProblemCodeOrderNotCancellable,
			"Заказ уже нельзя отменить", transition.Error())
	case errors.As(err, &invalid):
		writeAdminProblem(w, r, http.StatusBadRequest, admin_v1.ProblemCodeValidationFailed,
			"Некорректный запрос", invalid.Reason)
	default:
		writeInternalFailure(a.log, w, r, err)
	}
}

// adminOrderDocument переводит заказ в схему админской спецификации.
//
// Схема своя, хотя заказ тот же самый: поверхности живут разными циклами выпуска, и
// общий на двоих документ означал бы, что правка клиентской спецификации меняет ответ
// админской. Внешние идентификаторы позиций не показываются и здесь — это реквизиты
// интеграции с заведением, а не часть заказа, каким его разбирает человек.
func adminOrderDocument(order domain.Order) admin_v1.Order {
	items := make([]admin_v1.OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, admin_v1.OrderItem{
			ProductId: item.ProductID,
			Name:      item.Name,
			Price:     int64(item.Price),
			Quantity:  item.Quantity,
		})
	}

	document := admin_v1.Order{
		Id:           order.ID,
		CustomerId:   order.CustomerID,
		RestaurantId: order.RestaurantID,
		Status:       admin_v1.OrderStatus(order.Status),
		Address:      order.Address,
		Items:        items,
		Total:        int64(order.Total),
		CreatedAt:    order.CreatedAt,
		UpdatedAt:    order.UpdatedAt,
	}

	// Полей отмены у неотменённого заказа нет вовсе: пустые значения сообщили бы, что
	// заказ кто-то отменил без причины.
	if order.CancelledBy != "" {
		cancelledBy := admin_v1.CancelledBy(order.CancelledBy)
		document.CancelledBy = &cancelledBy
	}
	if order.CancellationReason != "" {
		document.CancellationReason = &order.CancellationReason
	}

	return document
}
