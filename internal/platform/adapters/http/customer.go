package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/customer_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

const (
	// CustomerAPIPrefix — префикс клиентского API. Сами пути берутся из спецификации,
	// здесь только то, что в ней вынесено в адрес сервера.
	CustomerAPIPrefix = "/api/kitchen/v1/customer"

	// CustomerPageDefault — размер страницы витрины, когда клиент не просил своего.
	// Значение сверяется со спецификацией тестом: разойтись с документом, по которому
	// живут клиенты, оно не может.
	CustomerPageDefault = 20
	// CustomerPageMax — потолок страницы витрины, объявленный контрактом.
	CustomerPageMax = 100

	// maxRequestBodyBytes — потолок на тело входящего запроса. Заказ — это список позиций
	// корзины, и мегабайта ему хватает с запасом; всё, что больше, площадка не разбирает,
	// а отклоняет: тело неограниченного размера читается в память до того, как хоть одно
	// правило предметной области успеет сказать, что запрос неверен.
	maxRequestBodyBytes = 1 * httpx.MB
)

// customerAPI обслуживает клиентское API площадки.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
type customerAPI struct {
	showcase *application.Showcase
	log      *slog.Logger
}

// mountCustomerAPI монтирует клиентское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountCustomerAPI(r chi.Router, showcase *application.Showcase, log *slog.Logger) {
	api := customerAPI{showcase: showcase, log: log}

	handler := customer_v1.NewStrictHandlerWithOptions(api, nil, customer_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writeCustomerRequestProblem,
		ResponseErrorHandlerFunc: api.writeCustomerResponseProblem,
	})

	// Лимит тела вешается на клиентское API, а не на весь роутер: отказ отдаётся по схеме
	// клиентской спецификации, а у партнёрского и админского API схема своя. Он стоит
	// перед сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, refuseOversizedCustomerBody))

		customer_v1.HandlerWithOptions(handler, customer_v1.ChiServerOptions{
			BaseURL:    CustomerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: writeCustomerRequestProblem,
		})
	})
}

// ListRestaurants отдаёт страницу списка работающих заведений.
//
// Меню в выдачу не попадает: карточка заведения и его меню — разные ресурсы, и меню
// со своей пагинацией не помещается ни в список, ни в карточку.
func (a customerAPI) ListRestaurants(
	ctx context.Context, request customer_v1.ListRestaurantsRequestObject,
) (customer_v1.ListRestaurantsResponseObject, error) {
	page, err := domain.NewPage(
		request.Params.Limit, request.Params.Offset, CustomerPageDefault, CustomerPageMax)
	if err != nil {
		return customer_v1.ListRestaurants400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerValidationProblem(err)),
		}, nil
	}

	catalog, err := a.showcase.Restaurants(ctx, page)
	if err != nil {
		return nil, err
	}

	items := make([]customer_v1.Restaurant, 0, len(catalog.Restaurants))
	for _, restaurant := range catalog.Restaurants {
		items = append(items, customer_v1.Restaurant{Id: restaurant.ID, Name: restaurant.Name})
	}

	return customer_v1.ListRestaurants200JSONResponse{
		Items: items,
		Page: customer_v1.PageMeta{
			Limit:  page.Limit,
			Offset: page.Offset,
			Total:  catalog.Total,
		},
	}, nil
}

// GetRestaurant отдаёт карточку заведения по идентификатору.
func (a customerAPI) GetRestaurant(
	ctx context.Context, request customer_v1.GetRestaurantRequestObject,
) (customer_v1.GetRestaurantResponseObject, error) {
	restaurant, err := a.showcase.Restaurant(ctx, request.RestaurantId)

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return customer_v1.GetRestaurant404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: customer_v1.
				NotFoundApplicationProblemPlusJSONResponse(customerProblem(
					http.StatusNotFound, customer_v1.RestaurantNotFound, "Заведение не найдено",
					"заведение не существует или больше не работает")),
		}, nil
	case err != nil:
		return nil, err
	}

	return customer_v1.GetRestaurant200JSONResponse{Id: restaurant.ID, Name: restaurant.Name}, nil
}

// Операции ниже описаны контрактом, но их обработчики подставляются своими этапами.

func (customerAPI) PlaceOrder(
	context.Context, customer_v1.PlaceOrderRequestObject,
) (customer_v1.PlaceOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) GetOrder(
	context.Context, customer_v1.GetOrderRequestObject,
) (customer_v1.GetOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) CancelOrder(
	context.Context, customer_v1.CancelOrderRequestObject,
) (customer_v1.CancelOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) SearchProducts(
	context.Context, customer_v1.SearchProductsRequestObject,
) (customer_v1.SearchProductsResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) GetProduct(
	context.Context, customer_v1.GetProductRequestObject,
) (customer_v1.GetProductResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) ListRestaurantProducts(
	context.Context, customer_v1.ListRestaurantProductsRequestObject,
) (customer_v1.ListRestaurantProductsResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// customerProblem собирает отказ клиентского API. Тело — общая основа `problem+json`:
// своих полей у этих отказов нет, а тип сгенерирован по спецификации, поэтому ответ не
// может разойтись с контрактом.
func customerProblem(
	status int, code customer_v1.ProblemCode, title, detail string,
) customer_v1.ProblemBase {
	return customer_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	}
}

// customerValidationProblem собирает отказ по неверному запросу.
func customerValidationProblem(err error) customer_v1.ProblemBase {
	return customerProblem(http.StatusBadRequest, customer_v1.ValidationFailed,
		"Некорректные параметры запроса", err.Error())
}

// writeCustomerRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// нечисловой `limit`, неразбираемое тело.
//
// Тело, оборвавшееся на пределе, приезжает сюда же — разбор натыкается на него уже после
// middleware, — и отдаётся тем же отказом, что и объявленная длина сверх предела: причина
// у клиента одна, и знать, объявил он длину честно или прислал тело без длины вовсе, ему
// незачем.
func writeCustomerRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		refuseOversizedCustomerBody(w, r)

		return
	}

	problem.Write(r.Context(), w, http.StatusBadRequest, customerValidationProblem(err))
}

// refuseOversizedCustomerBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет. Заводить новый код ради размера тела незачем —
// ветка поведения у клиента та же самая, что и при любом непринятом запросе.
func refuseOversizedCustomerBody(w http.ResponseWriter, r *http.Request) {
	problem.Write(r.Context(), w, http.StatusRequestEntityTooLarge, customerProblem(
		http.StatusRequestEntityTooLarge, customer_v1.ValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes)))
}

// writeCustomerResponseProblem отвечает, когда обработчик вернул ошибку. Операция,
// описанная контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
//
// Причина отказа по вине площадки уходит в лог, а не клиенту: наружу она рассказала бы об
// устройстве службы, а выбрать по ней ветку поведения клиент всё равно не может — ему
// остаётся повторить запрос позже.
func (a customerAPI) writeCustomerResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, httpx.ErrNotImplemented) {
		problem.Write(r.Context(), w, http.StatusNotImplemented, customerProblem(
			http.StatusNotImplemented, customer_v1.NotImplemented,
			"Операция ещё не реализована", err.Error()))

		return
	}

	a.log.ErrorContext(r.Context(), "customer request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, customerProblem(
		http.StatusInternalServerError, customer_v1.InternalError,
		"Внутренняя ошибка площадки", "повторите запрос позже"))
}
