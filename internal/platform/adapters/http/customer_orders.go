package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

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
// заведения оставляет заказ в `under_review`. Повтор того же заказа отвечает `200` и тем
// же заказом: второй заказ не заводится, и по коду ответа клиент видит, случилось ли
// что-то новое.
func (a customerAPI) PlaceOrder(
	ctx context.Context, request customer_v1.PlaceOrderRequestObject,
) (customer_v1.PlaceOrderResponseObject, error) {
	if request.Body == nil {
		return customer_v1.PlaceOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerProblemBody(
					http.StatusBadRequest, customer_v1.ValidationFailed,
					"Некорректный запрос", "тело запроса обязательно")),
		}, nil
	}

	items := make([]application.OrderedItem, 0, len(request.Body.Items))
	for _, item := range request.Body.Items {
		items = append(items, application.OrderedItem{ProductID: item.ProductId, Quantity: item.Quantity})
	}

	order, placed, err := a.ordering.Place(ctx, application.Placement{
		OrderID:      request.Body.OrderId,
		CustomerID:   request.Params.UserId,
		RestaurantID: request.Body.RestaurantId,
		Address:      request.Body.Address,
		Items:        items,
	})
	if err != nil {
		return a.refusedPlacement(ctx, err)
	}

	// Повтор — не событие: заказ был создан раньше, и `201` сообщил бы клиенту, что
	// сейчас появился второй.
	if !placed {
		return customer_v1.PlaceOrder200JSONResponse(orderDocument(order)), nil
	}

	return customer_v1.PlaceOrder201JSONResponse(orderDocument(order)), nil
}

// refusedPlacement собирает ответ на неудавшееся оформление.
//
// Разбор ошибки общий с просмотром заказа, а выбор типа ответа — свой: у оформления
// допустимых кодов больше, и сгенерированные типы это правило держат.
func (a customerAPI) refusedPlacement(
	ctx context.Context, err error,
) (customer_v1.PlaceOrderResponseObject, error) {
	status, body := a.orderRefusal(ctx, err)

	switch status {
	case http.StatusBadRequest:
		return customer_v1.PlaceOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerProblemBase(body)),
		}, nil
	case http.StatusNotFound:
		return customer_v1.PlaceOrder404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: customer_v1.
				NotFoundApplicationProblemPlusJSONResponse(customerProblemBase(body)),
		}, nil
	case http.StatusConflict:
		return customer_v1.PlaceOrder409ApplicationProblemPlusJSONResponse{
			ConflictApplicationProblemPlusJSONResponse: customer_v1.
				ConflictApplicationProblemPlusJSONResponse(body),
		}, nil
	case http.StatusUnprocessableEntity:
		return customer_v1.PlaceOrder422ApplicationProblemPlusJSONResponse{
			UnprocessableEntityApplicationProblemPlusJSONResponse: customer_v1.
				UnprocessableEntityApplicationProblemPlusJSONResponse(body),
		}, nil
	case http.StatusBadGateway:
		return customer_v1.PlaceOrder502ApplicationProblemPlusJSONResponse{
			BadGatewayApplicationProblemPlusJSONResponse: customer_v1.
				BadGatewayApplicationProblemPlusJSONResponse(body),
		}, nil
	case http.StatusGatewayTimeout:
		return customer_v1.PlaceOrder504ApplicationProblemPlusJSONResponse{
			GatewayTimeoutApplicationProblemPlusJSONResponse: customer_v1.
				GatewayTimeoutApplicationProblemPlusJSONResponse(body),
		}, nil
	default:
		return nil, err
	}
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
					BadRequestApplicationProblemPlusJSONResponse(customerProblemBase(body)),
			}, nil
		case http.StatusNotFound:
			return customer_v1.GetOrder404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: customer_v1.
					NotFoundApplicationProblemPlusJSONResponse(customerProblemBase(body)),
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
//
// Тело собирается расширенной схемой: отказ заведения называет товар и его новую цену, а
// там, где контракт обещает одну лишь основу, обработчик сужает тело до неё.
func (a customerAPI) orderRefusal(ctx context.Context, err error) (int, customer_v1.Problem) {
	var (
		invalid     domain.InvalidOrderError
		refused     domain.OrderRefusedError
		unavailable application.RestaurantUnavailableError
	)

	switch {
	case errors.As(err, &invalid):
		return http.StatusBadRequest, customerFailure(customerProblemBody(http.StatusBadRequest,
			customer_v1.ValidationFailed, "Некорректный запрос", invalid.Reason))
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return http.StatusNotFound, customerFailure(customerProblemBody(http.StatusNotFound,
			customer_v1.RestaurantNotFound, "Заведение не найдено",
			"заведение не существует или больше не работает"))
	case errors.Is(err, domain.ErrProductNotFound):
		return http.StatusNotFound, customerFailure(customerProblemBody(http.StatusNotFound,
			customer_v1.ProductNotFound, "Товар не найден", "товара нет в меню этого заведения"))
	case errors.Is(err, domain.ErrOrderNotFound):
		return http.StatusNotFound, customerFailure(customerProblemBody(http.StatusNotFound,
			customer_v1.OrderNotFound, "Заказ не найден",
			"заказа с таким идентификатором у вас нет"))
	case errors.As(err, &refused):
		return a.restaurantRefusal(ctx, refused)
	case errors.Is(err, domain.ErrIdempotencyKeyReuse):
		return http.StatusUnprocessableEntity, customerFailure(customerProblemBody(
			http.StatusUnprocessableEntity, customer_v1.IdempotencyKeyReuse,
			"Идентификатор заказа уже использован",
			"этим идентификатором уже оформлен другой заказ: выдайте новый и повторите"))
	case errors.Is(err, domain.ErrOrderIDTaken):
		// Чем именно занят идентификатор, покупателю не сообщается: по разным ответам
		// перебор рассказал бы, какие заказы на площадке есть и чем они отличаются от
		// присланного. Действие у покупателя всё равно одно — оформить заказ заново.
		return http.StatusConflict, customerFailure(customerProblemBody(http.StatusConflict,
			customer_v1.IdempotencyKeyReuse, "Идентификатор заказа уже занят",
			"этим идентификатором уже оформлен заказ: выдайте новый и повторите"))
	case errors.As(err, &unavailable):
		return a.unavailableRestaurantRefusal(ctx, unavailable)
	}

	return http.StatusInternalServerError, customer_v1.Problem{}
}

// restaurantRefusal отвечает покупателю отказом заведения.
//
// Код ответа один на все причины — `409`: заказ не выполнить в том виде, в каком его
// оформили, и различает причины машиночитаемое поле `code`, а не код ответа. Товар, из-за
// которого отказан заказ, уезжает тем же документом: без него клиенту пришлось бы гадать,
// какую строку корзины показать покупателю.
//
// Объяснение площадка пишет своё, а слова заведения оставляет в логе: `detail` заведения
// написан для того, кто разбирается в интеграции («площадка показала 45000, у заведения
// 90000»), а не для человека, который выбирает, брать ли круассан по новой цене.
func (a customerAPI) restaurantRefusal(
	ctx context.Context, refusal domain.OrderRefusedError,
) (int, customer_v1.Problem) {
	code, described := refusalCode(refusal.Reason)
	if !described {
		// Причина, которой в спецификации нет, — это расхождение площадки с собственным
		// контрактом, а не ответ покупателю: отвечает за него общая ветка внутренней
		// ошибки, а разбираться с ней приходит тот, кто читает лог.
		return http.StatusInternalServerError, customer_v1.Problem{}
	}

	a.log.WarnContext(ctx, "restaurant refused the order", slog.Any("error", refusal))

	title, detail := refusalWording(refusal.Reason)
	document := customerFailure(customerProblemBody(http.StatusConflict, code, title, detail))

	// Товара нет у отказа закрытого заведения, а цены — у всех отказов, кроме
	// подорожания: пустые значения сообщили бы клиенту, что позиция стоит ноль.
	if refusal.ProductID != uuid.Nil {
		document.ProductId = &refusal.ProductID
	}
	if refusal.Reason == domain.PriceChanged {
		price := int64(refusal.CurrentPrice)
		document.CurrentPrice = &price
	}

	return http.StatusConflict, document
}

// refusalCode переводит причину отказа предметной области в код клиентской спецификации.
//
// Перевод явный, а не приведением строки: перечень кодов спецификации закрыт, клиент
// вправе разбирать его как перечисление, и причина, которой в спецификации нет, — это
// расхождение площадки с собственным контрактом, а не ответ покупателю.
func refusalCode(reason domain.RefusalReason) (customer_v1.ProblemCode, bool) {
	switch reason {
	case domain.OutOfStock:
		return customer_v1.OutOfStock, true
	case domain.PriceChanged:
		return customer_v1.PriceChanged, true
	case domain.UnknownProduct:
		return customer_v1.UnknownProduct, true
	case domain.RestaurantClosed:
		return customer_v1.RestaurantClosed, true
	default:
		return "", false
	}
}

// refusalWording — как отказ заведения объясняется покупателю: коротким названием и
// пояснением, из которого видно, что делать дальше.
func refusalWording(reason domain.RefusalReason) (title, detail string) {
	switch reason {
	case domain.OutOfStock:
		return "Товар закончился", "заведение не может отдать столько: замените позицию и попробуйте снова"
	case domain.PriceChanged:
		return "Цена изменилась", "заведение отдаёт товар по другой цене — актуальная цена в поле current_price"
	case domain.UnknownProduct:
		return "Товара больше нет", "заведение убрало этот товар из меню: он вот-вот исчезнет и с витрины"
	case domain.RestaurantClosed:
		return "Заведение закрыто", "заведение сейчас не принимает заказы: попробуйте позже или выберите другое"
	default:
		return "Заказ не принят", "заведение не приняло заказ"
	}
}

// customerFailure достраивает основу отказа до расширенной схемы. Часть ответов
// клиентской спецификации умеет говорить больше — например, называть актуальную цену
// товара, — и сгенерированный тип у них поэтому другой.
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

// customerProblemBase сужает отказ до общей основы — её ждут те ответы, которым
// спецификация не разрешает говорить о товаре и цене (`400`, `404`). Своих полей у таких
// отказов и нет: назвать товар может только отказ заведения.
func customerProblemBase(document customer_v1.Problem) customer_v1.ProblemBase {
	return customer_v1.ProblemBase{
		Code:     document.Code,
		Detail:   document.Detail,
		Instance: document.Instance,
		Status:   document.Status,
		Title:    document.Title,
		Type:     document.Type,
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
) (int, customer_v1.Problem) {
	a.log.WarnContext(ctx, "restaurant did not accept the order", slog.Any("error", unavailable))

	if unavailable.TimedOut {
		return http.StatusGatewayTimeout, customerFailure(customerProblemBody(http.StatusGatewayTimeout,
			customer_v1.RestaurantUnavailable, "Заведение не ответило вовремя",
			"заказ не передан: заведение не ответило за отведённое время"))
	}

	return http.StatusBadGateway, customerFailure(customerProblemBody(http.StatusBadGateway,
		customer_v1.RestaurantUnavailable, "Заведение недоступно",
		"заказ не передан: заведение не отвечает"))
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
