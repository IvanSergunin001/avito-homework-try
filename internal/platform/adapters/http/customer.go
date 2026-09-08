package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

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

	// customerMaxSearchQuery — предел длины поисковой подстроки из той же спецификации.
	customerMaxSearchQuery = 200

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
	catalog  *application.Catalog
	ordering *application.Ordering
	log      *slog.Logger
}

// mountCustomerAPI монтирует клиентскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountCustomerAPI(
	r chi.Router,
	showcase *application.Showcase,
	catalog *application.Catalog,
	ordering *application.Ordering,
	log *slog.Logger,
) {
	api := customerAPI{showcase: showcase, catalog: catalog, ordering: ordering, log: log}

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
			ErrorHandlerFunc: refuseInvalidParameters,
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
		refuseInvalidParameters(w, r, err)

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

// ListRestaurantProducts отдаёт страницу меню заведения.
//
// Тот же ресурс сужает поиск до одного заведения: меню и поиск внутри заведения — одна и
// та же выдача с одним и тем же порядком, отличается только отбор по названию, и разводить
// их по разным адресам значило бы обещать клиенту разницу, которой нет.
func (a customerAPI) ListRestaurantProducts(
	w http.ResponseWriter, r *http.Request,
	restaurantID customer_v1.RestaurantId, params customer_v1.ListRestaurantProductsParams,
) {
	search, err := resolveProductSearch(params.Q, params.Limit, params.Offset)
	if err != nil {
		refuseInvalidParameters(w, r, err)

		return
	}

	page, err := a.catalog.Menu(r.Context(), restaurantID, search.nameContains, search.window())

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.RestaurantNotFound,
			"Заведение не найдено", "заведение не существует или больше не работает")

		return
	case err != nil:
		a.writeCustomerFailure(w, r, err)

		return
	}

	writeProductPage(w, r, page, search)
}

// SearchProducts отдаёт страницу выдачи поиска по всем работающим заведениям сразу.
//
// Заведение здесь не спрашивается специально: покупатель ищет блюдо, а не место, и
// требовать от него сначала выбрать заведение значило бы отвечать не на его вопрос.
func (a customerAPI) SearchProducts(w http.ResponseWriter, r *http.Request, params customer_v1.SearchProductsParams) {
	search, err := resolveProductSearch(params.Q, params.Limit, params.Offset)
	if err != nil {
		refuseInvalidParameters(w, r, err)

		return
	}

	page, err := a.catalog.Search(r.Context(), search.nameContains, search.window())
	if err != nil {
		a.writeCustomerFailure(w, r, err)

		return
	}

	writeProductPage(w, r, page, search)
}

// GetProduct отдаёт карточку товара по идентификатору.
func (a customerAPI) GetProduct(w http.ResponseWriter, r *http.Request, productID customer_v1.ProductId) {
	product, err := a.catalog.Product(r.Context(), productID)

	switch {
	case errors.Is(err, domain.ErrProductNotFound):
		writeCustomerProblem(w, r, http.StatusNotFound, customer_v1.ProductNotFound,
			"Товар не найден", "товара не существует или его заведение больше не работает")

		return
	case err != nil:
		a.writeCustomerFailure(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, productCard(product))
}

// productSearch — разобранные параметры выдачи товаров.
type productSearch struct {
	// nameContains — подстрока названия; пустая означает выдачу без отбора по названию.
	nameContains string
	// paging — окно выдачи.
	paging httpx.Paging
}

// window переводит окно в термины сценария.
func (s productSearch) window() application.Window {
	return application.Window{Limit: s.paging.Limit, Offset: s.paging.Offset}
}

// resolveProductSearch разбирает параметры, общие у меню заведения и у поиска по всем
// заведениям: окно выдачи и подстроку названия.
//
// Длина подстроки проверяется здесь по той же причине, по которой здесь же проверяются
// границы страницы: кодогенератор разбирает параметры, но не сверяет их с объявленными
// в спецификации ограничениями, а обещание контракта должно выполняться.
func resolveProductSearch(query *customer_v1.SearchQuery, limit, offset *int) (productSearch, error) {
	paging, err := httpx.ResolvePaging(limit, offset, customerDefaultPageSize, customerMaxPageSize)
	if err != nil {
		return productSearch{}, err
	}

	var nameContains string
	if query != nil {
		// Пробелы по краям — след ввода, а не часть искомого названия.
		nameContains = strings.TrimSpace(*query)
	}
	// Предел объявлен в знаках, а не в байтах: в названиях кириллица, и байтовая длина
	// обрезала бы русский запрос вдвое раньше объявленного.
	if utf8.RuneCountInString(nameContains) > customerMaxSearchQuery {
		return productSearch{}, fmt.Errorf("q: подстрока названия не длиннее %d знаков", customerMaxSearchQuery)
	}

	return productSearch{nameContains: nameContains, paging: paging}, nil
}

// writeProductPage отдаёт страницу выдачи товаров в схеме клиентской спецификации.
func writeProductPage(w http.ResponseWriter, r *http.Request, page application.ProductPage, search productSearch) {
	items := make([]customer_v1.Product, 0, len(page.Products))
	for _, product := range page.Products {
		items = append(items, productCard(product))
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, customer_v1.ProductPage{
		Items: items,
		Page: customer_v1.PageMeta{
			Limit:  search.paging.Limit,
			Offset: search.paging.Offset,
			Total:  page.Total,
		},
	})
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

// refuseInvalidParameters отказывает в запросе, параметры которого не прошли проверку:
// нечисловой `limit`, страница больше объявленного контрактом потолка, неверный uuid в
// пути. Причина уходит клиенту текстом: он в состоянии её исправить, а «некорректный
// запрос» без объяснения не подсказывает, что именно чинить.
func refuseInvalidParameters(w http.ResponseWriter, r *http.Request, err error) {
	writeCustomerProblem(w, r, http.StatusBadRequest, customer_v1.ValidationFailed,
		"Некорректные параметры запроса", err.Error())
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

// writeCustomerFailure отвечает на отказ по вине площадки: он одинаков на всех её
// поверхностях — без причины наружу и с причиной в логе.
func (a customerAPI) writeCustomerFailure(w http.ResponseWriter, r *http.Request, err error) {
	writeInternalFailure(a.log, w, r, err)
}

// internalProblem — отказ по вине площадки. Он остаётся в формате problem+json, но без
// поля `code`: перечень кодов закрыт спецификацией, и машиночитаемой причины у такого
// отказа нет — клиент различает его по коду ответа.
type internalProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
}
