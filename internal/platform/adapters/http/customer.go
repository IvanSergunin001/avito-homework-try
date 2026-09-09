package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx/problem"
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

	// CustomerMaxSearchQuery — предел длины поисковой подстроки, объявленный контрактом.
	// Сверяется со спецификацией тем же тестом, что и границы страницы: кодогенератор
	// `maxLength` в тип не переносит, и разойтись с документом константа могла бы молча.
	CustomerMaxSearchQuery = 200

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
	showcase     *application.Showcase
	catalog      *application.Catalog
	ordering     *application.Ordering
	cancellation *application.Cancellation
	log          *slog.Logger
}

// mountCustomerAPI монтирует клиентское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountCustomerAPI(
	r chi.Router,
	showcase *application.Showcase,
	catalog *application.Catalog,
	ordering *application.Ordering,
	cancellation *application.Cancellation,
	log *slog.Logger,
) {
	api := customerAPI{
		showcase: showcase, catalog: catalog, ordering: ordering,
		cancellation: cancellation, log: log,
	}

	handler := customer_v1.NewStrictHandlerWithOptions(api, nil, customer_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  customerProblems.BadRequest,
		ResponseErrorHandlerFunc: api.writeCustomerFailure,
	})

	// Лимит тела вешается на клиентское API, а не на весь роутер: отказ отдаётся по схеме
	// клиентской спецификации, а у партнёрского и админского API схема своя. Он стоит
	// перед сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, customerProblems.Oversized))

		customer_v1.HandlerWithOptions(handler, customer_v1.ChiServerOptions{
			BaseURL:    CustomerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: customerProblems.BadRequest,
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
				BadRequestApplicationProblemPlusJSONResponse(customerProblems.Validation(err)),
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
				NotFoundApplicationProblemPlusJSONResponse(restaurantNotFoundProblem()),
		}, nil
	case err != nil:
		return nil, err
	}

	return customer_v1.GetRestaurant200JSONResponse{Id: restaurant.ID, Name: restaurant.Name}, nil
}

// ListRestaurantProducts отдаёт страницу меню заведения.
//
// Тот же ресурс сужает поиск до одного заведения: меню и поиск внутри заведения — одна и
// та же выдача с одним и тем же порядком, отличается только отбор по названию, и разводить
// их по разным адресам значило бы обещать клиенту разницу, которой нет.
func (a customerAPI) ListRestaurantProducts(
	ctx context.Context, request customer_v1.ListRestaurantProductsRequestObject,
) (customer_v1.ListRestaurantProductsResponseObject, error) {
	search, err := resolveProductSearch(request.Params.Q, request.Params.Limit, request.Params.Offset)
	if err != nil {
		return customer_v1.ListRestaurantProducts400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerProblems.Validation(err)),
		}, nil
	}

	page, err := a.catalog.Menu(ctx, request.RestaurantId, search.filter())

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return customer_v1.ListRestaurantProducts404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: customer_v1.
				NotFoundApplicationProblemPlusJSONResponse(restaurantNotFoundProblem()),
		}, nil
	case err != nil:
		return nil, err
	}

	return customer_v1.ListRestaurantProducts200JSONResponse(productPage(page, search)), nil
}

// SearchProducts отдаёт страницу выдачи поиска по всем работающим заведениям сразу.
//
// Заведение здесь не спрашивается специально: покупатель ищет блюдо, а не место, и
// требовать от него сначала выбрать заведение значило бы отвечать не на его вопрос.
func (a customerAPI) SearchProducts(
	ctx context.Context, request customer_v1.SearchProductsRequestObject,
) (customer_v1.SearchProductsResponseObject, error) {
	search, err := resolveProductSearch(request.Params.Q, request.Params.Limit, request.Params.Offset)
	if err != nil {
		return customer_v1.SearchProducts400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: customer_v1.
				BadRequestApplicationProblemPlusJSONResponse(customerProblems.Validation(err)),
		}, nil
	}

	page, err := a.catalog.Search(ctx, search.filter())
	if err != nil {
		return nil, err
	}

	return customer_v1.SearchProducts200JSONResponse(productPage(page, search)), nil
}

// GetProduct отдаёт карточку товара по идентификатору.
func (a customerAPI) GetProduct(
	ctx context.Context, request customer_v1.GetProductRequestObject,
) (customer_v1.GetProductResponseObject, error) {
	product, err := a.catalog.Product(ctx, request.ProductId)

	switch {
	case errors.Is(err, domain.ErrProductNotFound):
		return customer_v1.GetProduct404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: customer_v1.
				NotFoundApplicationProblemPlusJSONResponse(customerProblemBody(
					http.StatusNotFound, customer_v1.ProductNotFound, "Товар не найден",
					"товара не существует или его заведение больше не работает")),
		}, nil
	case err != nil:
		return nil, err
	}

	return customer_v1.GetProduct200JSONResponse(productCard(product)), nil
}

// productSearch — разобранные параметры выдачи товаров.
type productSearch struct {
	// nameContains — подстрока названия; пустая означает выдачу без отбора по названию.
	nameContains string
	// page — окно выдачи, уже проверенное доменом.
	page domain.Page
}

// filter — отбор выдачи в том виде, в каком его принимает каталог.
func (s productSearch) filter() application.ProductFilter {
	return application.ProductFilter{NameContains: s.nameContains, Page: s.page}
}

// resolveProductSearch разбирает параметры, общие у меню заведения и у поиска по всем
// заведениям: окно выдачи и подстроку названия.
//
// Границы окна проверяет домен (`domain.NewPage`) — это правило площадки, а не подробность
// HTTP. Длина подстроки проверяется здесь и по той же причине, по которой границы вообще
// приходится проверять: кодогенератор разбирает параметры, но не сверяет их с объявленными
// в спецификации ограничениями, а обещание контракта должно выполняться.
func resolveProductSearch(query *customer_v1.SearchQuery, limit, offset *int) (productSearch, error) {
	page, err := domain.NewPage(limit, offset, CustomerPageDefault, CustomerPageMax)
	if err != nil {
		return productSearch{}, err
	}

	var asked string
	if query != nil {
		asked = *query
	}
	// Предел проверяется до обрезки — по тому, что прислал клиент: контракт обещает
	// `maxLength` для параметра запроса, а не для того, что от него осталось после
	// нормализации, иначе строку длиннее объявленного площадка молча принимала бы.
	// Предел объявлен в знаках, а не в байтах: в названиях кириллица, и байтовая длина
	// обрезала бы русский запрос вдвое раньше объявленного.
	if utf8.RuneCountInString(asked) > CustomerMaxSearchQuery {
		return productSearch{}, fmt.Errorf("q: подстрока названия не длиннее %d знаков", CustomerMaxSearchQuery)
	}

	// Пробелы по краям — след ввода, а не часть искомого названия; обрезка обещана
	// описанием параметра в спецификации.
	return productSearch{nameContains: strings.TrimSpace(asked), page: page}, nil
}

// productPage собирает страницу выдачи товаров в схеме клиентской спецификации.
func productPage(page application.ProductPage, search productSearch) customer_v1.ProductPage {
	items := make([]customer_v1.Product, 0, len(page.Products))
	for _, product := range page.Products {
		items = append(items, productCard(product))
	}

	return customer_v1.ProductPage{
		Items: items,
		Page: customer_v1.PageMeta{
			Limit:  search.page.Limit,
			Offset: search.page.Offset,
			Total:  page.Total,
		},
	}
}

// productCard переводит товар в карточку клиентской спецификации.
//
// Цена уходит клиенту тем же целым числом копеек, каким хранится: перевод в дробное
// число по дороге означал бы, что на витрине и в чеке разные деньги.
func productCard(product domain.Product) customer_v1.Product {
	card := customer_v1.Product{
		Id:           product.ID,
		RestaurantId: product.RestaurantID,
		Name:         product.Name,
		Price:        int64(product.Price),
		IsAvailable:  product.IsAvailable,
	}

	// Описания у позиции может не быть — тогда поля нет и в ответе: пустая строка
	// сообщила бы клиенту, что описание есть и оно пустое.
	if product.Description != "" {
		card.Description = &product.Description
	}

	return card
}

// customerProblemBody собирает тело отказа клиентского API — общую основу
// `problem+json`. Своих полей у этих отказов нет, а тип сгенерирован по спецификации,
// поэтому ответ не может разойтись с контрактом.
func customerProblemBody(
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

// customerProblems — отказы клиентского API. Форма у них та же, что у остальных четырёх
// поверхностей проекта, поэтому она живёт одна на всех (problem.Writer), а здесь названо
// только то, чем клиентское API от них отличается: сгенерированные типы, коды и тексты.
var customerProblems = problem.Writer[customer_v1.ProblemBase, customer_v1.ProblemCode]{
	Body: customerProblemBody,
	Codes: problem.Codes[customer_v1.ProblemCode]{
		ValidationFailed: customer_v1.ValidationFailed,
		NotImplemented:   customer_v1.NotImplemented,
		InternalError:    customer_v1.InternalError,
	},
	MaxRequestBytes: maxRequestBodyBytes,
	FailureTitle:    "Внутренняя ошибка площадки",
	Surface:         "customer",
}

// restaurantNotFoundProblem — отказ «заведения нет». Он одинаков у карточки заведения и у
// его меню: обёртки ответов разные, а причина и текст, который читает человек, — одни, и
// править их в двух местах значило бы однажды разойтись.
func restaurantNotFoundProblem() customer_v1.ProblemBase {
	return customerProblemBody(http.StatusNotFound, customer_v1.RestaurantNotFound,
		"Заведение не найдено", "заведение не существует или больше не работает")
}

// writeCustomerFailure отвечает, когда обработчик вернул ошибку: `501` на операцию,
// описанную контрактом, но ещё не реализованную, и `500` на всё остальное. Форма отказа
// общая, а логгер у каждого адаптера свой — за ним метод и заводится.
func (a customerAPI) writeCustomerFailure(w http.ResponseWriter, r *http.Request, err error) {
	customerProblems.Failure(a.log, w, r, err)
}
