package http

import (
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
	// customerBaseURL — префикс клиентской поверхности. Сами пути берутся из
	// спецификации, здесь только то, что в ней вынесено в адрес сервера.
	customerBaseURL = "/api/v1"

	// Границы страницы витрины повторяют объявленные в клиентской спецификации.
	customerDefaultPageSize = 20
	customerMaxPageSize     = 100

	// maxRequestBodyBytes — потолок на тело входящего запроса. Заказ — это список позиций
	// корзины, и мегабайта ему хватает с запасом; всё, что больше, площадка не разбирает,
	// а отклоняет: тело неограниченного размера читается в память до того, как хоть одно
	// правило предметной области успеет сказать, что запрос неверен.
	maxRequestBodyBytes = 1 << 20
)

// customerAPI обслуживает клиентскую поверхность площадки.
//
// Встроенная сгенерированная заглушка отвечает `501` на операции, до которых очередь
// ещё не дошла: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе и перекрывает заглушку.
type customerAPI struct {
	customer_v1.Unimplemented

	showcase *application.Showcase
	log      *slog.Logger
}

// mountCustomerAPI монтирует клиентскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountCustomerAPI(r chi.Router, showcase *application.Showcase, log *slog.Logger) {
	api := customerAPI{showcase: showcase, log: log}

	// Лимит тела вешается на поверхность, а не на весь роутер: отказ отдаётся по схеме
	// клиентской спецификации, а у партнёрской и админской поверхностей схема своя.
	// Он стоит перед сгенерированными маршрутами, потому что размер тела — это то, что
	// проверяется раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, api.refuseOversizedBody))

		customer_v1.HandlerWithOptions(api, customer_v1.ChiServerOptions{
			BaseURL:    customerBaseURL,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
					"Некорректные параметры запроса", err.Error())
			},
		})
	})
}

// ListRestaurants отдаёт страницу списка работающих заведений.
//
// Меню в выдачу не попадает: карточка заведения и его меню — разные ресурсы, и меню
// со своей пагинацией не помещается ни в список, ни в карточку.
func (a customerAPI) ListRestaurants(w http.ResponseWriter, r *http.Request, params customer_v1.ListRestaurantsParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, customerDefaultPageSize, customerMaxPageSize)
	if err != nil {
		writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
			"Некорректные параметры запроса", err.Error())

		return
	}

	page, err := a.showcase.Restaurants(r.Context(), application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		a.writeCustomerFailure(w, r, err)

		return
	}

	items := make([]customer_v1.Restaurant, 0, len(page.Restaurants))
	for _, restaurant := range page.Restaurants {
		items = append(items, customer_v1.Restaurant{Id: restaurant.ID, Name: restaurant.Name})
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, customer_v1.RestaurantPage{
		Items: items,
		Page: customer_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
		},
	})
}

// GetRestaurant отдаёт карточку заведения по идентификатору.
func (a customerAPI) GetRestaurant(w http.ResponseWriter, r *http.Request, restaurantID customer_v1.RestaurantId) {
	restaurant, err := a.showcase.Restaurant(r.Context(), restaurantID)

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.RestaurantNotFound,
			"Заведение не найдено", "заведение не существует или больше не работает")

		return
	case err != nil:
		a.writeCustomerFailure(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, customer_v1.Restaurant{
		Id:   restaurant.ID,
		Name: restaurant.Name,
	})
}

// refuseOversizedBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет. Заводить новый код ради размера тела незачем —
// ветка поведения у клиента та же самая, что и при любом непринятом запросе.
func (a customerAPI) refuseOversizedBody(w http.ResponseWriter, r *http.Request) {
	writeCustomerProblem(w, r, http.StatusRequestEntityTooLarge, customer_v1.ValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes))
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

// writeCustomerFailure отвечает на отказ по вине площадки. Причина уходит в лог, а не
// клиенту: наружу она рассказала бы об устройстве службы, а выбрать по ней ветку
// поведения клиент всё равно не может — ему остаётся повторить запрос позже.
func (a customerAPI) writeCustomerFailure(w http.ResponseWriter, r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "customer request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, internalProblem{
		Type:   problem.DefaultType,
		Title:  "Внутренняя ошибка",
		Status: http.StatusInternalServerError,
	})
}

// internalProblem — отказ по вине площадки. Он остаётся в формате problem+json, но без
// поля `code`: перечень кодов закрыт спецификацией, и машиночитаемой причины у такого
// отказа нет — клиент различает его по коду ответа.
type internalProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
}
