package http

import (
	"context"
	"errors"
	"net/http"

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
func (a adminAPI) CancelOrderBySupport(
	ctx context.Context, request admin_v1.CancelOrderBySupportRequestObject,
) (admin_v1.CancelOrderBySupportResponseObject, error) {
	if request.Body == nil {
		return admin_v1.CancelOrderBySupport400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: admin_v1.
				BadRequestApplicationProblemPlusJSONResponse(adminProblemBody(
					http.StatusBadRequest, admin_v1.ValidationFailed,
					"Некорректный запрос", "тело запроса обязательно")),
		}, nil
	}

	// Незаполненная причина сюда не проверяется: её ждёт домен, и отказ у отмены без
	// причины и у отмены с причиной из одних пробелов получается один и тот же.
	order, err := a.cancellation.BySupport(ctx, request.OrderId, request.Body.Reason)
	if err != nil {
		return a.refusedCancellation(err)
	}

	return admin_v1.CancelOrderBySupport200JSONResponse(adminOrderDocument(order)), nil
}

// refusedCancellation переводит ожидаемый отказ отмены в типизированный ответ контракта.
//
// Отказ матрицы переходов доезжает отдельным кодом причины: `order_not_cancellable` —
// это ответ «заказ в таком состоянии не отменяют», и по нему саппорт видит, что заказ
// уже доставлен или отменён раньше, а не что он неверно составил запрос.
func (a adminAPI) refusedCancellation(
	err error,
) (admin_v1.CancelOrderBySupportResponseObject, error) {
	var (
		invalid    domain.InvalidOrderError
		transition domain.OrderTransitionError
	)

	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		return admin_v1.CancelOrderBySupport404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: admin_v1.
				NotFoundApplicationProblemPlusJSONResponse(adminProblemBody(
					http.StatusNotFound, admin_v1.OrderNotFound, "Заказ не найден",
					"заказа с таким идентификатором на площадке нет")),
		}, nil
	case errors.As(err, &transition):
		return admin_v1.CancelOrderBySupport409ApplicationProblemPlusJSONResponse{
			ConflictApplicationProblemPlusJSONResponse: admin_v1.
				ConflictApplicationProblemPlusJSONResponse(adminProblemBody(
					http.StatusConflict, admin_v1.OrderNotCancellable,
					"Заказ уже нельзя отменить", transition.Error())),
		}, nil
	case errors.As(err, &invalid):
		return admin_v1.CancelOrderBySupport400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: admin_v1.
				BadRequestApplicationProblemPlusJSONResponse(adminProblemBody(
					http.StatusBadRequest, admin_v1.ValidationFailed,
					"Некорректный запрос", invalid.Reason)),
		}, nil
	default:
		return nil, err
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
