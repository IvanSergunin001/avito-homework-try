package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/customer_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Заказ на клиентской поверхности: оформление и просмотр. Обе операции живут отдельным
// файлом от витрины не по слоям, а по предмету: витрина отвечает на вопрос «что можно
// заказать», заказ — на вопрос «что со мной происходит».

// PlaceOrder оформляет заказ покупателя.
//
// Идентификатор заказа приходит от клиента: он же служит ключом идемпотентности, поэтому
// придумать его должна та сторона, которая повторяет запрос. Суммы в запросе нет вовсе —
// её считает площадка по своему каталогу, и присланной клиентом сумме взяться неоткуда.
//
// Обязательные поля тела не проверяются здесь: тело разбирает сгенерированный обработчик,
// а незаполненное поле доезжает до домена и возвращается оттуда причиной отказа — так
// формулировка отказа остаётся одна на все входы, а не повторяется на границе.
//
// Ответ — `201`: заказ создан. Что с ним дальше, видно по статусу: успешный ответ
// заведения оставляет заказ в `under_review`.
func (a customerAPI) PlaceOrder(
	ctx context.Context, request customer_v1.PlaceOrderRequestObject,
) (customer_v1.PlaceOrderResponseObject, error) {
	if request.Body == nil {
		return customer_v1.PlaceOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerProblem(
					http.StatusBadRequest, customer_v1.ValidationFailed,
					"Некорректный запрос", "тело запроса обязательно")),
		}, nil
	}

	items := make([]application.OrderedItem, 0, len(request.Body.Items))
	for _, item := range request.Body.Items {
		items = append(items, application.OrderedItem{ProductID: item.ProductId, Quantity: item.Quantity})
	}

	order, err := a.ordering.Place(ctx, application.Placement{
		OrderID:      request.Body.OrderId,
		CustomerID:   request.Params.UserId,
		RestaurantID: request.Body.RestaurantId,
		Address:      request.Body.Address,
		Items:        items,
	})
	if err != nil {
		status, body := a.orderRefusal(ctx, err)

		switch status {
		case http.StatusBadRequest:
			return customer_v1.PlaceOrder400ApplicationProblemPlusJSONResponse{
				BadRequestApplicationProblemPlusJSONResponse: customer_v1.
					BadRequestApplicationProblemPlusJSONResponse(body),
			}, nil
		case http.StatusNotFound:
			return customer_v1.PlaceOrder404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: customer_v1.
					NotFoundApplicationProblemPlusJSONResponse(body),
			}, nil
		case http.StatusBadGateway:
			return customer_v1.PlaceOrder502ApplicationProblemPlusJSONResponse{
				BadGatewayApplicationProblemPlusJSONResponse: customer_v1.
					BadGatewayApplicationProblemPlusJSONResponse(customerFailure(body)),
			}, nil
		case http.StatusGatewayTimeout:
			return customer_v1.PlaceOrder504ApplicationProblemPlusJSONResponse{
				GatewayTimeoutApplicationProblemPlusJSONResponse: customer_v1.
					GatewayTimeoutApplicationProblemPlusJSONResponse(customerFailure(body)),
			}, nil
		default:
			return nil, err
		}
	}

	return customer_v1.PlaceOrder201JSONResponse(orderDocument(order)), nil
}

// GetOrder отдаёт заказ покупателя: его состояние, состав и сумму.
//
// Состав и сумма отдаются такими, какими были в момент оформления: обновление каталога
// историю покупателя не переписывает. Чужой заказ неотличим от несуществующего — иначе
// перебор идентификаторов рассказал бы, какие заказы на площадке есть.
func (a customerAPI) GetOrder(
	ctx context.Context, request customer_v1.GetOrderRequestObject,
) (customer_v1.GetOrderResponseObject, error) {
	order, err := a.ordering.Order(ctx, request.OrderId, request.Params.UserId)
	if err != nil {
		status, body := a.orderRefusal(ctx, err)

		switch status {
		case http.StatusBadRequest:
			return customer_v1.GetOrder400ApplicationProblemPlusJSONResponse{
				BadRequestApplicationProblemPlusJSONResponse: customer_v1.
					BadRequestApplicationProblemPlusJSONResponse(body),
			}, nil
		case http.StatusNotFound:
			return customer_v1.GetOrder404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: customer_v1.
					NotFoundApplicationProblemPlusJSONResponse(body),
			}, nil
		default:
			return nil, err
		}
	}

	return customer_v1.GetOrder200JSONResponse(orderDocument(order)), nil
}

// orderRefusal переводит ошибку сценария заказа в код ответа и тело `problem+json`.
//
// Разбор ошибок стоит отдельно от обработчиков, а тип ответа собирает каждый из них сам:
// у операции свой набор допустимых кодов, и сгенерированные типы это правило держат —
// вернуть из просмотра заказа отказ «заведение недоступно» попросту нечем.
func (a customerAPI) orderRefusal(ctx context.Context, err error) (int, customer_v1.ProblemBase) {
	var (
		invalid     domain.InvalidOrderError
		unavailable application.RestaurantUnavailableError
	)

	switch {
	case errors.As(err, &invalid):
		return http.StatusBadRequest, customerProblem(http.StatusBadRequest,
			customer_v1.ValidationFailed, "Некорректный запрос", invalid.Reason)
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return http.StatusNotFound, customerProblem(http.StatusNotFound,
			customer_v1.RestaurantNotFound, "Заведение не найдено",
			"заведение не существует или больше не работает")
	case errors.Is(err, domain.ErrProductNotFound):
		return http.StatusNotFound, customerProblem(http.StatusNotFound,
			customer_v1.ProductNotFound, "Товар не найден", "товара нет в меню этого заведения")
	case errors.Is(err, domain.ErrOrderNotFound):
		return http.StatusNotFound, customerProblem(http.StatusNotFound,
			customer_v1.OrderNotFound, "Заказ не найден",
			"заказа с таким идентификатором у вас нет")
	case errors.As(err, &unavailable):
		return a.unavailableRestaurantRefusal(ctx, unavailable)
	}

	return http.StatusInternalServerError, customer_v1.ProblemBase{}
}

// customerFailure достраивает основу отказа до расширенной схемы. Часть ответов
// клиентской спецификации умеет говорить больше — например, называть актуальную цену
// товара, — и сгенерированный тип у них поэтому другой. Отказы заказа этих полей не
// заполняют: заведению, которое не ответило, сказать про цену нечего.
func customerFailure(base customer_v1.ProblemBase) customer_v1.Problem {
	return customer_v1.Problem{
		Code:     base.Code,
		Detail:   base.Detail,
		Instance: base.Instance,
		Status:   base.Status,
		Title:    base.Title,
		Type:     base.Type,
	}
}

// unavailableRestaurantRefusal отвечает покупателю, что заведение не ответило на его заказ.
//
// Код ответа разный, а машиночитаемая причина одна: молчащее заведение — сорванный вызов
// (`502`), не уложившееся в отведённое время — истёкшее ожидание (`504`). Различие
// касается того, стоит ли повторять запрос, и потому доезжает до клиента; что именно
// случилось у заведения, остаётся в логе площадки — выбрать по этому ветку поведения
// покупатель всё равно не может.
func (a customerAPI) unavailableRestaurantRefusal(
	ctx context.Context, unavailable application.RestaurantUnavailableError,
) (int, customer_v1.ProblemBase) {
	a.log.WarnContext(ctx, "restaurant did not accept the order", slog.Any("error", unavailable))

	if unavailable.TimedOut {
		return http.StatusGatewayTimeout, customerProblem(http.StatusGatewayTimeout,
			customer_v1.RestaurantUnavailable, "Заведение не ответило вовремя",
			"заказ не передан: заведение не ответило за отведённое время")
	}

	return http.StatusBadGateway, customerProblem(http.StatusBadGateway,
		customer_v1.RestaurantUnavailable, "Заведение недоступно",
		"заказ не передан: заведение не отвечает")
}

// orderDocument переводит заказ в схему клиентской спецификации.
//
// Деньги уезжают тем же целым числом копеек, каким хранятся: перевод в дробное число по
// дороге означал бы, что в заказе и в чеке разные суммы. Внешние идентификаторы позиций
// покупателю не показываются — это реквизиты интеграции с заведением, а не часть заказа,
// каким его видит человек.
func orderDocument(order domain.Order) customer_v1.Order {
	items := make([]customer_v1.OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, customer_v1.OrderItem{
			ProductId: item.ProductID,
			Name:      item.Name,
			Price:     int64(item.Price),
			Quantity:  item.Quantity,
		})
	}

	document := customer_v1.Order{
		Id:           order.ID,
		CustomerId:   order.CustomerID,
		RestaurantId: order.RestaurantID,
		Status:       customer_v1.OrderStatus(order.Status),
		Address:      order.Address,
		Items:        items,
		Total:        int64(order.Total),
		CreatedAt:    order.CreatedAt,
		UpdatedAt:    order.UpdatedAt,
	}

	// Полей отмены у неотменённого заказа нет вовсе: пустые значения сообщили бы клиенту,
	// что заказ кто-то отменил без причины.
	if order.CancelledBy != "" {
		cancelledBy := customer_v1.CancelledBy(order.CancelledBy)
		document.CancelledBy = &cancelledBy
	}
	if order.CancellationReason != "" {
		document.CancellationReason = &order.CancellationReason
	}

	return document
}
