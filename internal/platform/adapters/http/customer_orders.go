package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
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
// Ответ — `201`: заказ создан. Что с ним дальше, видно по статусу: успешный ответ
// заведения оставляет заказ в `under_review`. Повтор того же заказа отвечает `200` и тем
// же заказом: второй заказ не заводится, и по коду ответа клиент видит, случилось ли
// что-то новое.
func (a customerAPI) PlaceOrder(w http.ResponseWriter, r *http.Request, params customer_v1.PlaceOrderParams) {
	var body customer_v1.NewOrder
	if err := decodeOrderBody(r, &body, "order_id", "restaurant_id", "address", "items"); err != nil {
		a.refuseOrder(w, r, err)

		return
	}

	items := make([]application.OrderedItem, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, application.OrderedItem{ProductID: item.ProductId, Quantity: item.Quantity})
	}

	order, placed, err := a.ordering.Place(r.Context(), application.Placement{
		OrderID:      body.OrderId,
		CustomerID:   params.UserId,
		RestaurantID: body.RestaurantId,
		Address:      body.Address,
		Items:        items,
	})
	if err != nil {
		a.refuseOrder(w, r, err)

		return
	}

	status := http.StatusOK
	if placed {
		status = http.StatusCreated
	}

	httpx.WriteJSON(r.Context(), w, status, orderDocument(order))
}

// GetOrder отдаёт заказ покупателя: его состояние, состав и сумму.
//
// Состав и сумма отдаются такими, какими были в момент оформления: обновление каталога
// историю покупателя не переписывает. Чужой заказ неотличим от несуществующего — иначе
// перебор идентификаторов рассказал бы, какие заказы на площадке есть.
func (a customerAPI) GetOrder(
	w http.ResponseWriter, r *http.Request, orderID customer_v1.OrderId, params customer_v1.GetOrderParams,
) {
	order, err := a.ordering.Order(r.Context(), orderID, params.UserId)
	if err != nil {
		a.refuseOrder(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, orderDocument(order))
}

// CancelOrder отменяет заказ по требованию покупателя.
//
// Тела у запроса нет: причину покупатель не называет — он передумал, и объяснять это
// никому не обязан. Ответ — сам заказ: по нему видно и то, что он отменён, и то, кем, —
// если отмена случилась раньше и не по воле покупателя.
//
// Повторная отмена отвечает тем же `200` и тем же заказом: ретрай кнопки «Отменить» не
// должен ломаться. Заказ, который уже готовят, отменить нельзя — `409` с кодом
// `order_not_cancellable`: продукты потрачены, и такие случаи разбирает саппорт.
func (a customerAPI) CancelOrder(
	w http.ResponseWriter, r *http.Request, orderID customer_v1.OrderId, params customer_v1.CancelOrderParams,
) {
	order, err := a.cancellation.ByCustomer(r.Context(), orderID, params.UserId)
	if err != nil {
		a.refuseOrder(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, orderDocument(order))
}

// decodeOrderBody разбирает тело запроса заказа и переводит отказ разбора в ошибку
// предметной области: причину читает разработчик клиента, и отвечает ему та же ветка, что
// и на неверно описанный заказ.
func decodeOrderBody(r *http.Request, body any, required ...string) error {
	if err := decodeBody(r, body, required...); err != nil {
		return domain.InvalidOrderError{Reason: err.Error()}
	}

	return nil
}

// refuseOrder отвечает на ошибку сценария заказа. Разбор в одном месте — обработчики
// повторяют не разбор ошибок, а только свой сценарий.
func (a customerAPI) refuseOrder(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid     domain.InvalidOrderError
		refused     domain.OrderRefusedError
		transition  domain.OrderTransitionError
		unavailable application.RestaurantUnavailableError
	)

	switch {
	case errors.As(err, &invalid):
		writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
			"Некорректный запрос", invalid.Reason)
	case errors.Is(err, domain.ErrRestaurantNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.RestaurantNotFound,
			"Заведение не найдено", "заведение не существует или больше не работает")
	case errors.Is(err, domain.ErrProductNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.ProductNotFound,
			"Товар не найден", "товара нет в меню этого заведения")
	case errors.Is(err, domain.ErrOrderNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.OrderNotFound,
			"Заказ не найден", "заказа с таким идентификатором у вас нет")
	case errors.As(err, &refused):
		a.refuseByTheRestaurant(w, r, refused)
	case errors.As(err, &transition) && transition.To == domain.Cancelled:
		// Единственный переход, которого требует покупатель, — отмена: других ручек,
		// двигающих заказ, на клиентской поверхности нет. Отказ матрицы в ней означает
		// одно и то же: заказ уже готовят, и продукты на него потрачены. Названный
		// переход в условии — не перестраховка: отказ в каком-нибудь другом переходе
		// был бы поломкой самой площадки, и выдавать её покупателю за правило нельзя.
		writeCustomerProblem(w, r, http.StatusConflict, customer_v1.OrderNotCancellable,
			"Заказ уже нельзя отменить",
			"заведение начало готовить заказ: продукты потрачены — обратитесь в поддержку")
	case errors.Is(err, domain.ErrIdempotencyKeyReuse):
		writeCustomerProblem(w, r, http.StatusUnprocessableEntity, customer_v1.IdempotencyKeyReuse,
			"Идентификатор заказа уже использован",
			"этим идентификатором уже оформлен другой заказ: выдайте новый и повторите")
	case errors.Is(err, domain.ErrOrderIDTaken):
		// Чем именно занят идентификатор, покупателю не сообщается: по разным ответам
		// перебор рассказал бы, какие заказы на площадке есть и чем они отличаются от
		// присланного. Действие у покупателя всё равно одно — оформить заказ заново.
		writeCustomerProblem(w, r, http.StatusConflict, customer_v1.IdempotencyKeyReuse,
			"Идентификатор заказа уже занят",
			"этим идентификатором уже оформлен заказ: выдайте новый и повторите")
	case errors.As(err, &unavailable):
		a.refuseUnavailableRestaurant(w, r, unavailable)
	default:
		a.writeCustomerFailure(w, r, err)
	}
}

// refuseByTheRestaurant отвечает покупателю отказом заведения.
//
// Код ответа один на все причины — `409`: заказ не выполнить в том виде, в каком его
// оформили, и различает причины машиночитаемое поле `code`, а не код ответа. Товар, из-за
// которого отказан заказ, уезжает тем же документом: без него клиенту пришлось бы гадать,
// какую строку корзины показать покупателю.
//
// Объяснение площадка пишет своё, а слова заведения оставляет в логе: `detail` заведения
// написан для того, кто разбирается в интеграции («площадка показала 45000, у заведения
// 90000»), а не для человека, который выбирает, брать ли круассан по новой цене.
func (a customerAPI) refuseByTheRestaurant(
	w http.ResponseWriter, r *http.Request, refusal domain.OrderRefusedError,
) {
	code, described := refusalCode(refusal.Reason)
	if !described {
		a.writeCustomerFailure(w, r, refusal)

		return
	}

	a.log.WarnContext(r.Context(), "restaurant refused the order", slog.Any("error", refusal))

	title, detail := refusalWording(refusal.Reason)
	document := customer_v1.Problem{
		Type:   problem.DefaultType,
		Title:  title,
		Status: http.StatusConflict,
		Detail: &detail,
		Code:   code,
	}
	// Товара нет у отказа закрытого заведения, а цены — у всех отказов, кроме
	// подорожания: пустые значения сообщили бы клиенту, что позиция стоит ноль.
	if refusal.ProductID != uuid.Nil {
		document.ProductId = &refusal.ProductID
	}
	if refusal.Reason == domain.PriceChanged {
		price := int64(refusal.CurrentPrice)
		document.CurrentPrice = &price
	}

	problem.Write(r.Context(), w, http.StatusConflict, document)
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

// refuseUnavailableRestaurant отвечает покупателю, что заведение не ответило на его заказ.
//
// Код ответа разный, а машиночитаемая причина одна: молчащее заведение — сорванный вызов
// (`502`), не уложившееся в отведённое время — истёкшее ожидание (`504`). Различие
// касается того, стоит ли повторять запрос, и потому доезжает до клиента; что именно
// случилось у заведения, остаётся в логе площадки — выбрать по этому ветку поведения
// покупатель всё равно не может.
func (a customerAPI) refuseUnavailableRestaurant(
	w http.ResponseWriter, r *http.Request, unavailable application.RestaurantUnavailableError,
) {
	a.log.WarnContext(r.Context(), "restaurant did not accept the order", slog.Any("error", unavailable))

	if unavailable.TimedOut {
		writeCustomerProblem(w, r, http.StatusGatewayTimeout, customer_v1.RestaurantUnavailable,
			"Заведение не ответило вовремя", "заказ не передан: заведение не ответило за отведённое время")

		return
	}

	writeCustomerProblem(w, r, http.StatusBadGateway, customer_v1.RestaurantUnavailable,
		"Заведение недоступно", "заказ не передан: заведение не отвечает")
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
