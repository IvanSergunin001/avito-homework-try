package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

const (
	// PartnerAPIPrefix — префикс партнёрского контракта в коробке. Сегмент говорит, чья
	// это сторона: обязательства заведения живут под `restaurant`, ручки площадки —
	// под `partner` на самой площадке.
	PartnerAPIPrefix = "/api/kitchen/v1/restaurant"

	// MenuPageDefault — размер страницы меню, когда площадка не просила своего;
	// значение сверяется со спецификацией тестом.
	MenuPageDefault = 100
	// MenuPageMax — потолок страницы меню, объявленный партнёрским контрактом.
	MenuPageMax = 500

	// maxRequestBodyBytes — потолок на тело входящего запроса, один на обе поверхности
	// коробки. Заказ от площадки — это список позиций корзины, позиция меню от оператора —
	// название, описание и два числа: мегабайта хватает с запасом и тому, и другому. Всё,
	// что больше, коробка не разбирает, а отклоняет: тело неограниченного размера читается
	// в память до того, как хоть одно правило успеет сказать, что запрос неверен.
	maxRequestBodyBytes = 1 * httpx.MB
)

// partnerAPI обслуживает в коробке ту половину партнёрского контракта, которую обязано
// реализовать заведение. Коробка сдаёт тот же экзамен, что и заведение со своей системой:
// интерфейс сгенерирован по общему документу контракта.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
type partnerAPI struct {
	menu   *application.Menu
	orders *application.Orders
	log    *slog.Logger
}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, menu *application.Menu, orders *application.Orders, log *slog.Logger) {
	api := partnerAPI{menu: menu, orders: orders, log: log}

	handler := partner_v1.NewStrictHandlerWithOptions(api, nil, partner_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  partnerProblems.BadRequest,
		ResponseErrorHandlerFunc: api.writePartnerFailure,
	})

	// Лимит тела вешается на API, а не на весь роутер: отказ отдаётся по схеме
	// партнёрского контракта, а у рабочего места оператора схема своя. Он стоит перед
	// сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, partnerProblems.Oversized))

		partner_v1.HandlerWithOptions(handler, partner_v1.ChiServerOptions{
			BaseURL:    PartnerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`, отсутствующий заголовок идемпотентности. Контракт обещает на них
			// `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: partnerProblems.BadRequest,
		})
	})
}

// ListMenu отдаёт страницу меню заведения по партнёрскому контракту.
//
// Наружу уезжает меньше, чем видит оператор: остаток и отметка стоп-листа — внутренняя
// кухня заведения, площадке достаточно ответа «заказать можно или нельзя». Позиция,
// которой нет, из выдачи не пропадает: покупатель искал бы блюдо, которое вчера видел,
// и не находил его без объяснения.
func (a partnerAPI) ListMenu(
	ctx context.Context, request partner_v1.ListMenuRequestObject,
) (partner_v1.ListMenuResponseObject, error) {
	paging, err := httpx.ResolvePaging(
		request.Params.Limit, request.Params.Offset, MenuPageDefault, MenuPageMax)
	if err != nil {
		return partner_v1.ListMenu400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: partner_v1.
				BadRequestApplicationProblemPlusJSONResponse(partnerProblems.Validation(err)),
		}, nil
	}

	// Синхронизация каталога от неудачного ответа не страдает: выгрузка применяется
	// целиком, и незаконченная оставляет площадке прежний каталог.
	page, err := a.menu.Page(ctx, application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		return nil, err
	}

	items := make([]partner_v1.MenuItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, contractItem(item))
	}

	return partner_v1.ListMenu200JSONResponse{
		Items: items,
		Page: partner_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
		},
	}, nil
}

func (partnerAPI) CancelAcceptedOrder(
	context.Context, partner_v1.CancelAcceptedOrderRequestObject,
) (partner_v1.CancelAcceptedOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// contractItem переводит позицию меню в схему партнёрского контракта.
//
// Цена уезжает тем же целым числом копеек, каким хранится: перевод в дробное число по
// дороге означал бы, что у коробки и у витрины разные деньги.
func contractItem(item domain.MenuItem) partner_v1.MenuItem {
	return partner_v1.MenuItem{
		ExternalId:  item.ExternalID(),
		Name:        item.Name,
		Description: optional(item.Description),
		Price:       int64(item.Price),
		IsAvailable: item.IsAvailable(),
	}
}

// partnerProblemBody собирает тело отказа партнёрского контракта — общую основу
// `problem+json`. Своих полей у этих отказов нет, а тип сгенерирован по спецификации,
// поэтому ответ не может разойтись с контрактом.
func partnerProblemBody(
	status int, code partner_v1.ProblemCode, title, detail string,
) partner_v1.ProblemBase {
	return partner_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	}
}

// partnerProblems — отказы партнёрского контракта в коробке. Форма у них та же, что у
// остальных четырёх поверхностей проекта, поэтому она живёт одна на всех
// (problem.Writer), а здесь названо только то, чем эта половина контракта от них
// отличается: сгенерированные типы, коды и тексты.
var partnerProblems = problem.Writer[partner_v1.ProblemBase, partner_v1.ProblemCode]{
	Body: partnerProblemBody,
	Codes: problem.Codes[partner_v1.ProblemCode]{
		ValidationFailed: partner_v1.ValidationFailed,
		NotImplemented:   partner_v1.NotImplemented,
		InternalError:    partner_v1.InternalError,
	},
	MaxRequestBytes: maxRequestBodyBytes,
	FailureTitle:    "Внутренняя ошибка коробки",
	Surface:         "partner",
}

// writePartnerFailure отвечает, когда обработчик вернул ошибку: `501` на операцию,
// описанную контрактом, но ещё не реализованную, и `500` на всё остальное. Форма отказа
// общая, а логгер у каждого адаптера свой — за ним метод и заводится.
func (a partnerAPI) writePartnerFailure(w http.ResponseWriter, r *http.Request, err error) {
	partnerProblems.Failure(a.log, w, r, err)
}

// AcceptOrder принимает заказ, переданный площадкой по контракту.
//
// Синхронная проверка: коробка — источник истины по наличию и цене, и успешный ответ
// означает обязательство выполнить заказ, а не «мы посмотрим». Всё, что коробка выполнить
// не может, отдаётся отказом с машиночитаемой причиной, по которой площадка выбирает
// ветку поведения — обновить каталог, показать покупателю новую цену или увести его в
// другое заведение.
//
// Повторная отправка того же заказа опознаётся по ключу идемпотентности: второй заказ не
// заводится, остаток второй раз не списывается, а площадке отвечает результат первой
// попытки — `200` вместо `201`. Номер попытки приходит заголовком контракта и уходит в
// лог: по нему видно, что площадка ретраила и сколько раз, а поведение коробки от него не
// зависит — иначе повтор с потерянным номером готовился бы заново.
func (a partnerAPI) AcceptOrder(
	ctx context.Context, request partner_v1.AcceptOrderRequestObject,
) (partner_v1.AcceptOrderResponseObject, error) {
	if request.Body == nil {
		return acceptOrderBadRequest("Некорректный заказ", "тело запроса обязательно"), nil
	}

	body := *request.Body
	items := make([]domain.RequestedItem, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, domain.RequestedItem{
			ExternalID: item.ExternalId,
			Quantity:   item.Quantity,
			Price:      domain.Money(item.Price),
		})
	}

	orderRequest, err := domain.NewOrderRequest(body.OrderId, request.Params.IdempotencyKey,
		body.Address, items, domain.Money(body.Total), placedAt(body.PlacedAt))
	if err != nil {
		return a.acceptOrderFailure(err)
	}

	order, accepted, err := a.orders.Accept(ctx, orderRequest)
	if err != nil {
		return a.acceptOrderFailure(err)
	}

	a.log.InfoContext(ctx, "order delivered by platform",
		slog.String("order_id", order.ID.String()),
		slog.String("platform_order_id", order.PlatformOrderID.String()),
		slog.Int("attempt", request.Params.IdempotencyAttempt),
		slog.Bool("accepted_now", accepted),
	)

	// Заказ, заведённый этой доставкой, — `201`; повтор — `200` с тем же телом: по коду
	// ответа площадка видит, случилось ли что-то новое, а по телу — что именно у неё
	// приняли.
	response := partner_v1.OrderAcceptance{
		OrderId:    order.PlatformOrderID,
		AcceptedAt: order.AcceptedAt,
	}
	if accepted {
		return partner_v1.AcceptOrder201JSONResponse(response), nil
	}

	return partner_v1.AcceptOrder200JSONResponse(response), nil
}

// placedAt приводит время оформления заказа к значению: площадка не обязана его
// присылать, и «не сказали» здесь — это нулевое время, а не ошибка.
func placedAt(moment *time.Time) time.Time {
	if moment == nil {
		return time.Time{}
	}

	return *moment
}

// acceptOrderFailure переводит ожидаемый отказ сценария в типизированный ответ контракта.
func (a partnerAPI) acceptOrderFailure(err error) (partner_v1.AcceptOrderResponseObject, error) {
	var (
		refusal domain.OrderRefusedError
		invalid domain.InvalidOrderError
	)

	switch {
	case errors.As(err, &refusal):
		return acceptOrderRefusal(refusal)
	case errors.As(err, &invalid):
		return acceptOrderBadRequest("Некорректный заказ", invalid.Reason), nil
	default:
		return nil, err
	}
}

// refuseOrder отвечает площадке отказом по правилам предметной области.
//
// Код ответа один на все причины — `409`: контракт обещает на приёме заказа только его,
// и различает причины машиночитаемое поле `code`, а не код ответа. Позиция, из-за которой
// отказан заказ, и её актуальная цена уезжают тем же документом: без них площадке
// пришлось бы гадать, что чинить в каталоге.
func acceptOrderRefusal(
	refusal domain.OrderRefusedError,
) (partner_v1.AcceptOrderResponseObject, error) {
	code, described := refusalCode(refusal.Reason)
	if !described {
		return nil, refusal
	}

	document := partner_v1.Problem{
		Type:   problem.DefaultType,
		Title:  refusalTitle(refusal.Reason),
		Status: http.StatusConflict,
		Detail: &refusal.Detail,
		Code:   code,
	}
	if refusal.ProductExternalID != "" {
		document.ProductExternalId = &refusal.ProductExternalID
	}
	if refusal.Reason == domain.PriceChanged {
		price := int64(refusal.CurrentPrice)
		document.CurrentPrice = &price
	}

	return partner_v1.AcceptOrder409ApplicationProblemPlusJSONResponse{
		ConflictApplicationProblemPlusJSONResponse: partner_v1.
			ConflictApplicationProblemPlusJSONResponse(document),
	}, nil
}

func acceptOrderBadRequest(title, detail string) partner_v1.AcceptOrderResponseObject {
	document := partnerProblemBody(http.StatusBadRequest, partner_v1.ValidationFailed, title, detail)

	return partner_v1.AcceptOrder400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: partner_v1.
			BadRequestApplicationProblemPlusJSONResponse(document),
	}
}

// refusalCode переводит причину отказа предметной области в код партнёрского контракта.
//
// Перевод явный, а не приведением строки: перечень кодов контракта закрыт, обе стороны
// вправе разбирать его как перечисление, и причина, которой в контракте нет, — это
// расхождение коробки с контрактом, а не ответ площадке.
func refusalCode(reason domain.RefusalReason) (partner_v1.ProblemCode, bool) {
	switch reason {
	case domain.RestaurantClosed:
		return partner_v1.RestaurantClosed, true
	case domain.UnknownProduct:
		return partner_v1.UnknownProduct, true
	case domain.OutOfStock:
		return partner_v1.OutOfStock, true
	case domain.PriceChanged:
		return partner_v1.PriceChanged, true
	default:
		return "", false
	}
}

// refusalTitle — короткое название отказа для человека, который читает лог площадки.
func refusalTitle(reason domain.RefusalReason) string {
	switch reason {
	case domain.RestaurantClosed:
		return "Заведение закрыто"
	case domain.UnknownProduct:
		return "Неизвестная позиция"
	case domain.OutOfStock:
		return "Позиции нет в наличии"
	case domain.PriceChanged:
		return "Цена изменилась"
	default:
		return "Заказ не принят"
	}
}
