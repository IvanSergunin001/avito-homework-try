package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/customer_v1"
)

const (
	// customerBaseURL — префикс клиентской поверхности. Сами пути берутся из
	// спецификации, здесь только то, что в ней вынесено в адрес сервера.
	customerBaseURL = "/api/v1"

	// Границы страницы витрины повторяют объявленные в клиентской спецификации.
	customerDefaultPageSize = 20
	customerMaxPageSize     = 100
)

// customerAPI обслуживает клиентскую поверхность площадки.
//
// Встроенная сгенерированная заглушка отвечает `501` на операции, до которых очередь
// ещё не дошла: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе и перекрывает заглушку.
type customerAPI struct {
	customer_v1.Unimplemented
}

// mountCustomerAPI монтирует клиентскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountCustomerAPI(r chi.Router) {
	customer_v1.HandlerWithOptions(customerAPI{}, customer_v1.ChiServerOptions{
		BaseURL:    customerBaseURL,
		BaseRouter: r,
		// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
		// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
				"Некорректные параметры запроса", err.Error())
		},
	})
}

// ListRestaurants отдаёт страницу списка заведений.
//
// Хранилище появляется на следующем этапе, поэтому витрина пока пуста. Пустая страница —
// не заглушка: это ровно тот ответ, который контракт обещает на запрос к каталогу, в
// котором нет ни одного заведения.
func (customerAPI) ListRestaurants(w http.ResponseWriter, r *http.Request, params customer_v1.ListRestaurantsParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, customerDefaultPageSize, customerMaxPageSize)
	if err != nil {
		writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
			"Некорректные параметры запроса", err.Error())

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, customer_v1.RestaurantPage{
		Items: []customer_v1.Restaurant{},
		Page: customer_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
		},
	})
}

// writeCustomerProblem отвечает отказом в схеме клиентской спецификации: тип
// сгенерирован по ней же, поэтому тело ответа не может разойтись с контрактом.
func writeCustomerProblem(
	w http.ResponseWriter, r *http.Request, status int, code customer_v1.ProblemCode, title, detail string,
) {
	problem.Write(r.Context(), w, status, customer_v1.Problem{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
