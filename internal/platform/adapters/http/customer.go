package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/customer_v1"
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
)

// customerAPI обслуживает клиентское API площадки.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
type customerAPI struct{}

// mountCustomerAPI монтирует клиентское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountCustomerAPI(r chi.Router) {
	handler := customer_v1.NewStrictHandlerWithOptions(customerAPI{}, nil, customer_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writeCustomerRequestProblem,
		ResponseErrorHandlerFunc: writeCustomerResponseProblem,
	})

	customer_v1.HandlerWithOptions(handler, customer_v1.ChiServerOptions{
		BaseURL:    CustomerAPIPrefix,
		BaseRouter: r,
		// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
		// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
		ErrorHandlerFunc: writeCustomerRequestProblem,
	})
}

// ListRestaurants отдаёт страницу списка заведений.
//
// Хранилище появляется на следующем этапе, поэтому витрина пока пуста. Пустая страница —
// не заглушка: это ровно тот ответ, который контракт обещает на запрос к каталогу, в
// котором нет ни одного заведения.
func (customerAPI) ListRestaurants(
	_ context.Context, request customer_v1.ListRestaurantsRequestObject,
) (customer_v1.ListRestaurantsResponseObject, error) {
	paging, err := httpx.ResolvePaging(
		request.Params.Limit, request.Params.Offset, CustomerPageDefault, CustomerPageMax)
	if err != nil {
		return customer_v1.ListRestaurants400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerValidationProblem(err)),
		}, nil
	}

	return customer_v1.ListRestaurants200JSONResponse{
		Items: []customer_v1.Restaurant{},
		Page: customer_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
		},
	}, nil
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

func (customerAPI) GetRestaurant(
	context.Context, customer_v1.GetRestaurantRequestObject,
) (customer_v1.GetRestaurantResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (customerAPI) ListRestaurantProducts(
	context.Context, customer_v1.ListRestaurantProductsRequestObject,
) (customer_v1.ListRestaurantProductsResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// customerValidationProblem собирает отказ по неверному запросу в схеме клиентского API:
// тип сгенерирован по ней же, поэтому тело ответа не может разойтись с контрактом.
func customerValidationProblem(err error) customer_v1.Problem {
	detail := err.Error()

	return customer_v1.Problem{
		Type:   problem.DefaultType,
		Title:  "Некорректные параметры запроса",
		Status: http.StatusBadRequest,
		Detail: &detail,
		Code:   customer_v1.ValidationFailed,
	}
}

// writeCustomerRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// нечисловой `limit`, неразбираемое тело.
func writeCustomerRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	problem.Write(r.Context(), w, http.StatusBadRequest, customerValidationProblem(err))
}

// writeCustomerResponseProblem отвечает, когда обработчик вернул ошибку. Операция,
// описанная контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
func writeCustomerResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	status, title, code := http.StatusInternalServerError, "Внутренняя ошибка площадки",
		customer_v1.InternalError
	if errors.Is(err, httpx.ErrNotImplemented) {
		status, title, code = http.StatusNotImplemented, "Операция ещё не реализована",
			customer_v1.NotImplemented
	}

	detail := err.Error()
	problem.Write(r.Context(), w, status, customer_v1.Problem{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
