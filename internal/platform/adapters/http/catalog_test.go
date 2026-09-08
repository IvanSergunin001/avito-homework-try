package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	customer "github.com/IvanSergunin001/Test157/api/customer/v1"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// productNames — названия товаров страницы в том порядке, в каком их отдала площадка.
func productNames(page customer.ProductPage) []string {
	return names(page.Items, func(item customer.Product) string { return item.Name })
}

// query — поисковая подстрока параметром выдачи.
func query(text string) *customer.SearchQuery {
	return &text
}

// menu запрашивает страницу меню заведения и требует, чтобы ответ был успешным.
func (p *platform) menu(
	t *testing.T, restaurant uuid.UUID, params *customer.ListRestaurantProductsParams,
) customer.ProductPage {
	t.Helper()

	resp, err := p.catalog(t).ListRestaurantProductsWithResponse(context.Background(), restaurant, params)
	if err != nil {
		t.Fatalf("запросить меню заведения: %v", err)
	}

	return gotBody(t, "меню заведения", "200", resp.JSON200, resp.HTTPResponse, resp.Body)
}

// missingMenu требует, чтобы меню заведения не нашлось, и возвращает отказ.
func (p *platform) missingMenu(t *testing.T, restaurant uuid.UUID) customer.NotFound {
	t.Helper()

	resp, err := p.catalog(t).ListRestaurantProductsWithResponse(context.Background(), restaurant,
		&customer.ListRestaurantProductsParams{})
	if err != nil {
		t.Fatalf("запросить меню заведения: %v", err)
	}

	return gotBody(t, "меню заведения", "404", resp.ApplicationproblemJSON404, resp.HTTPResponse, resp.Body)
}

// refusedMenu требует, чтобы меню отказало по неверным параметрам, и возвращает отказ.
func (p *platform) refusedMenu(
	t *testing.T, restaurant uuid.UUID, params *customer.ListRestaurantProductsParams,
) customer.BadRequest {
	t.Helper()

	resp, err := p.catalog(t).ListRestaurantProductsWithResponse(context.Background(), restaurant, params)
	if err != nil {
		t.Fatalf("запросить меню заведения: %v", err)
	}

	return gotBody(t, "меню заведения", "400", resp.ApplicationproblemJSON400, resp.HTTPResponse, resp.Body)
}

// searchPage запрашивает поиск по всем заведениям и требует, чтобы ответ был успешным.
func (p *platform) searchPage(t *testing.T, params *customer.SearchProductsParams) customer.ProductPage {
	t.Helper()

	resp, err := p.catalog(t).SearchProductsWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить поиск: %v", err)
	}

	return gotBody(t, "поиск", "200", resp.JSON200, resp.HTTPResponse, resp.Body)
}

// refusedSearch требует, чтобы поиск отказал по неверным параметрам, и возвращает отказ.
func (p *platform) refusedSearch(t *testing.T, params *customer.SearchProductsParams) customer.BadRequest {
	t.Helper()

	resp, err := p.catalog(t).SearchProductsWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить поиск: %v", err)
	}

	return gotBody(t, "поиск", "400", resp.ApplicationproblemJSON400, resp.HTTPResponse, resp.Body)
}

// product запрашивает карточку товара и требует, чтобы ответ был успешным.
func (p *platform) product(t *testing.T, id uuid.UUID) customer.Product {
	t.Helper()

	resp, err := p.catalog(t).GetProductWithResponse(context.Background(), id)
	if err != nil {
		t.Fatalf("запросить карточку товара: %v", err)
	}

	return gotBody(t, "карточка товара", "200", resp.JSON200, resp.HTTPResponse, resp.Body)
}

// missingProduct требует, чтобы карточка товара не нашлась, и возвращает отказ.
func (p *platform) missingProduct(t *testing.T, id uuid.UUID) customer.NotFound {
	t.Helper()

	resp, err := p.catalog(t).GetProductWithResponse(context.Background(), id)
	if err != nil {
		t.Fatalf("запросить карточку товара: %v", err)
	}

	return gotBody(t, "карточка товара", "404", resp.ApplicationproblemJSON404, resp.HTTPResponse, resp.Body)
}

func TestMenuIsGivenPageByPageWithTheWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	platform.addProduct(t, restaurant, menuItem{Name: "Круассан", Price: 12000})

	page := platform.menu(t, restaurant, &customer.ListRestaurantProductsParams{})

	if page.Page.Limit != 20 || page.Page.Offset != 0 {
		t.Errorf("страница меню limit=%d offset=%d, ожидали умолчания контракта 20 и 0",
			page.Page.Limit, page.Page.Offset)
	}
}

func TestMenuWindowWalksTheWholeMenuWithoutLosingOrRepeatingProducts(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	for _, name := range []string{"Багет", "Булочка", "Круассан", "Слойка", "Штрудель"} {
		platform.addProduct(t, restaurant, menuItem{Name: name, Price: 10000})
	}

	limit := 2

	var seen []string
	for _, offset := range []int{0, 2, 4} {
		page := platform.menu(t, restaurant,
			&customer.ListRestaurantProductsParams{Limit: &limit, Offset: &offset})
		if page.Page.Total != 5 {
			t.Errorf("offset=%d: total=%d, ожидали 5 — общее число не зависит от окна", offset, page.Page.Total)
		}
		if len(page.Items) > limit {
			t.Errorf("offset=%d: на странице %d позиций, а просили не больше двух", offset, len(page.Items))
		}
		seen = append(seen, productNames(page)...)
	}

	slices.Sort(seen)
	want := []string{"Багет", "Булочка", "Круассан", "Слойка", "Штрудель"}
	if !slices.Equal(seen, want) {
		t.Errorf("страницы вместе дали %v, ожидали каждую позицию ровно по разу: %v", seen, want)
	}
}

func TestMenuRefusesPageLargerThanContractAllows(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")

	limit := platformhttp.CustomerPageMax + 1

	document := platform.refusedMenu(t, restaurant, &customer.ListRestaurantProductsParams{Limit: &limit})

	if document.Code != customer.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
	if document.Detail == nil || *document.Detail == "" {
		t.Error("в отказе нет объяснения, а понятная ошибка валидации должна называть предел")
	}
}

func TestMenuShowsOnlyTheProductsOfTheRestaurantItWasAskedFor(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := platform.openRestaurant(t, "Пекарня «Батон»")
	coffee := platform.openRestaurant(t, "Кофейня «Пар»")
	platform.addProduct(t, bakery, menuItem{Name: "Круассан", Price: 12000})
	platform.addProduct(t, coffee, menuItem{Name: "Капучино", Price: 25000})

	page := platform.menu(t, bakery, &customer.ListRestaurantProductsParams{})

	if got := productNames(page); !slices.Equal(got, []string{"Круассан"}) {
		t.Errorf("в меню пекарни %v, ожидали только её «Круассан»", got)
	}
}

func TestMenuOfRestaurantThatStoppedWorkingIsNotFoundInsteadOfBeingEmpty(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.closedRestaurant(t, "Столовая «Обед»")
	platform.addProduct(t, restaurant, menuItem{Name: "Компот", Price: 5000})

	document := platform.missingMenu(t, restaurant)

	if document.Code != customer.RestaurantNotFound {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.RestaurantNotFound)
	}
}

func TestMenuOfRestaurantThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).missingMenu(t,
		uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a01"))

	if document.Code != customer.RestaurantNotFound {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.RestaurantNotFound)
	}
}

func TestProductCardTellsWhatIsBoughtAndForHowMuch(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	id := platform.addProduct(t, restaurant, menuItem{
		Name:        "Круассан с миндалём",
		Description: "Слоёное тесто, миндальный крем",
		Price:       45000,
	})

	card := platform.product(t, id)

	if card.Id != id || card.RestaurantId != restaurant {
		t.Errorf("карточка отдала товар %q заведения %q, ожидали %q и %q",
			card.Id, card.RestaurantId, id, restaurant)
	}
	if card.Name != "Круассан с миндалём" {
		t.Errorf("название %q, ожидали «Круассан с миндалём»", card.Name)
	}
	if card.Description == nil || *card.Description != "Слоёное тесто, миндальный крем" {
		t.Errorf("описание %v, ожидали «Слоёное тесто, миндальный крем»", card.Description)
	}
	if card.Price != 45000 {
		t.Errorf("цена %d, ожидали 45000 копеек", card.Price)
	}
}

// Деньги ходят целыми копейками: дробное число, посчитанное в двоичной дроби, расходится
// с чеком, а строка заставляла бы клиента разбирать формат.
func TestPriceReachesTheClientAsAWholeNumberOfMinorUnits(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	id := platform.addProduct(t, restaurant, menuItem{Name: "Круассан", Price: 45000})

	var fields map[string]json.RawMessage
	card := platformhttp.CustomerAPIPrefix + "/products/" + id.String()
	if err := json.Unmarshal(platform.get(t, card).Body, &fields); err != nil {
		t.Fatalf("разобрать карточку товара: %v", err)
	}

	if got := string(fields["price"]); got != "45000" {
		t.Errorf("цена пришла как %s, ожидали целое 45000: копейки не переводятся ни в рубли, ни в строку", got)
	}
}

// Товар в стоп-листе остаётся на витрине: убери его — и покупатель искал бы блюдо,
// которое вчера видел, не понимая, куда оно делось.
func TestProductInStopListStaysVisibleAndIsMarkedAsOutOfStock(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	id := platform.addProduct(t, restaurant, menuItem{Name: "Круассан", Price: 12000, OutOfStock: true})

	if card := platform.product(t, id); card.IsAvailable {
		t.Error("карточка товара из стоп-листа говорит, что он в наличии")
	}

	page := platform.menu(t, restaurant, &customer.ListRestaurantProductsParams{})
	if len(page.Items) != 1 {
		t.Fatalf("в меню %d позиций, ожидали одну: стоп-лист не убирает товар с витрины", len(page.Items))
	}
	if page.Items[0].IsAvailable {
		t.Error("в меню товар из стоп-листа помечен как доступный")
	}
}

func TestProductWithoutDescriptionIsShownWithoutOne(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	id := platform.addProduct(t, restaurant, menuItem{Name: "Круассан", Price: 12000})

	if card := platform.product(t, id); card.Description != nil {
		t.Errorf("описание пришло как %q, а заведение его не давало", *card.Description)
	}
}

func TestCardOfProductThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).missingProduct(t,
		uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a02"))

	if document.Code != customer.ProductNotFound {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ProductNotFound)
	}
}

func TestCardOfProductOfRestaurantThatStoppedWorkingIsNotFoundEither(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.closedRestaurant(t, "Столовая «Обед»")
	id := platform.addProduct(t, restaurant, menuItem{Name: "Компот", Price: 5000})

	document := platform.missingProduct(t, id)

	if document.Code != customer.ProductNotFound {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ProductNotFound)
	}
}

func TestSearchLooksForADishAcrossAllRestaurantsAtOnce(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := platform.openRestaurant(t, "Пекарня «Батон»")
	coffee := platform.openRestaurant(t, "Кофейня «Пар»")
	platform.addProduct(t, bakery, menuItem{Name: "Круассан с миндалём", Price: 45000})
	platform.addProduct(t, coffee, menuItem{Name: "Круассан с шоколадом", Price: 39000})
	platform.addProduct(t, coffee, menuItem{Name: "Капучино", Price: 25000})

	page := platform.searchPage(t, &customer.SearchProductsParams{Q: query("круассан")})

	got := productNames(page)
	slices.Sort(got)
	want := []string{"Круассан с миндалём", "Круассан с шоколадом"}
	if !slices.Equal(got, want) {
		t.Errorf("поиск нашёл %v, ожидали круассаны из обоих заведений: %v", got, want)
	}
	if page.Page.Total != 2 {
		t.Errorf("total=%d, ожидали 2: столько товаров подошло под запрос", page.Page.Total)
	}
}

func TestSearchCanBeNarrowedToASingleRestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := platform.openRestaurant(t, "Пекарня «Батон»")
	coffee := platform.openRestaurant(t, "Кофейня «Пар»")
	platform.addProduct(t, bakery, menuItem{Name: "Круассан с миндалём", Price: 45000})
	platform.addProduct(t, coffee, menuItem{Name: "Круассан с шоколадом", Price: 39000})

	page := platform.menu(t, bakery, &customer.ListRestaurantProductsParams{Q: query("круассан")})

	if got := productNames(page); !slices.Equal(got, []string{"Круассан с миндалём"}) {
		t.Errorf("поиск в пекарне нашёл %v, ожидали только её круассан", got)
	}
	if page.Page.Total != 1 {
		t.Errorf("total=%d, ожидали 1: выдача сужена одним заведением", page.Page.Total)
	}
}

// Покупатель набирает часть названия и в том регистре, в каком ему удобно.
func TestSearchFindsProductByPartOfItsNameRegardlessOfCase(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	platform.addProduct(t, restaurant, menuItem{Name: "Круассан с миндалём", Price: 45000})

	for _, text := range []string{"МИНДАЛ", "миндал", "уассан с"} {
		page := platform.searchPage(t, &customer.SearchProductsParams{Q: query(text)})
		if got := productNames(page); !slices.Equal(got, []string{"Круассан с миндалём"}) {
			t.Errorf("по запросу %q нашлось %v, ожидали «Круассан с миндалём»", text, got)
		}
	}
}

func TestSearchDoesNotShowProductsOfRestaurantsThatStoppedWorking(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	working := platform.openRestaurant(t, "Пекарня «Батон»")
	closed := platform.closedRestaurant(t, "Столовая «Обед»")
	platform.addProduct(t, working, menuItem{Name: "Круассан с миндалём", Price: 45000})
	platform.addProduct(t, closed, menuItem{Name: "Круассан вчерашний", Price: 1000})

	page := platform.searchPage(t, &customer.SearchProductsParams{Q: query("круассан")})

	if got := productNames(page); !slices.Equal(got, []string{"Круассан с миндалём"}) {
		t.Errorf("поиск нашёл %v, ожидали только круассан работающего заведения", got)
	}
	// Счётчик считает то же, что и выдача: иначе клиент листал бы страницы, на которых
	// ничего нет.
	if page.Page.Total != 1 {
		t.Errorf("total=%d, ожидали 1: товар закрытого заведения не считается", page.Page.Total)
	}
}

// Покупатель ищет текст, а не пишет шаблон: набрав «50%», он ищет скидку, а не «всё, где
// есть 50».
func TestSearchTreatsTheQueryAsTextAndNotAsAPattern(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	platform.addProduct(t, restaurant, menuItem{Name: "Набор 50% миндаля", Price: 45000})
	platform.addProduct(t, restaurant, menuItem{Name: "Набор 5090 грамм", Price: 39000})

	page := platform.searchPage(t, &customer.SearchProductsParams{Q: query("50%")})

	if got := productNames(page); !slices.Equal(got, []string{"Набор 50% миндаля"}) {
		t.Errorf("по запросу «50%%» нашлось %v, ожидали только «Набор 50%% миндаля»", got)
	}
}

func TestSearchWithoutQueryShowsTheWholeCatalog(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := platform.openRestaurant(t, "Пекарня «Батон»")
	coffee := platform.openRestaurant(t, "Кофейня «Пар»")
	platform.addProduct(t, bakery, menuItem{Name: "Круассан", Price: 45000})
	platform.addProduct(t, coffee, menuItem{Name: "Капучино", Price: 25000})

	for _, params := range []*customer.SearchProductsParams{{}, {Q: query("")}} {
		page := platform.searchPage(t, params)

		got := productNames(page)
		slices.Sort(got)
		if !slices.Equal(got, []string{"Капучино", "Круассан"}) {
			t.Errorf("поиск отдал %v, ожидали весь каталог: пустой запрос — это отсутствие запроса", got)
		}
	}
}

func TestSearchRefusesPageLargerThanContractAllows(t *testing.T) {
	t.Parallel()

	limit := platformhttp.CustomerPageMax + 1

	document := newPlatform(t).refusedSearch(t, &customer.SearchProductsParams{Limit: &limit})

	if document.Code != customer.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
	if document.Status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", document.Status)
	}
}

func TestSearchRefusesQueryLongerThanContractAllows(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusedSearch(t,
		&customer.SearchProductsParams{Q: query(strings.Repeat("к", 201))})

	if document.Code != customer.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
}

// Предел длины относится к тому, что прислал клиент: обрезка пробелов — удобство
// площадки, и прятать за ней превышение объявленного контрактом предела она не вправе.
func TestSearchCountsTheQueryLengthAsTheClientSentItAndNotAsItWasTrimmed(t *testing.T) {
	t.Parallel()

	asked := strings.Repeat(" ", 20) + strings.Repeat("к", 190)

	document := newPlatform(t).refusedSearch(t, &customer.SearchProductsParams{Q: query(asked)})

	if document.Code != customer.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", document.Code, customer.ValidationFailed)
	}
}

// Поиск по подстроке названия обычному B-tree не по зубам: подстрока может начинаться
// где угодно, и без отдельного индекса каждый запрос читал бы каталог целиком. Миграция
// заводит триграммный индекс, и поиск обязан идти через него — иначе на каталоге в
// тысячи позиций витрина превращается в таймаут.
func TestSearchByNameGoesThroughTheTrigramIndexAndNotThroughTheWholeCatalog(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	// Каталог должен быть достаточно большим, чтобы обход индекса стал дешевле чтения
	// таблицы: на десятке строк планировщик прочитает её целиком, и любой индекс
	// останется незамеченным — вместе с его отсутствием.
	platform.fillMenu(t, restaurant, catalogLargeEnoughToNeedAnIndex)
	platform.addProduct(t, restaurant, menuItem{Name: "Круассан с миндалём", Price: 45000})

	page := platform.searchPage(t, &customer.SearchProductsParams{Q: query("круассан")})
	if got := productNames(page); !slices.Equal(got, []string{"Круассан с миндалём"}) {
		t.Fatalf("поиск нашёл %v, ожидали «Круассан с миндалём»", got)
	}

	if !platform.indexWasUsed(t, "products_name_trgm_idx") {
		t.Error("поиск по названию прошёл мимо триграммного индекса: каталог перебирается целиком")
	}
}

// fillMenu набивает меню заведения позициями, названия которых под поиск не подходят.
func (p *platform) fillMenu(t *testing.T, restaurant uuid.UUID, count int) {
	t.Helper()

	_, err := p.db.ExecContext(t.Context(), `
		INSERT INTO platform.products (id, restaurant_id, external_id, name, price)
		SELECT gen_random_uuid(), $1, 'позиция-' || number, 'Позиция №' || number, 10000
		FROM generate_series(1, $2) AS number`, restaurant, count)
	if err != nil {
		t.Fatalf("набить меню: %v", err)
	}

	// Без свежей статистики планировщик оценивает таблицу по умолчанию и выбирает план
	// не по тем данным, которые в ней лежат.
	if _, err := p.db.ExecContext(t.Context(), `ANALYZE platform.products`); err != nil {
		t.Fatalf("обновить статистику: %v", err)
	}
}

// indexWasUsed сообщает, читала ли база указанный индекс.
//
// Счётчик обращений — единственный след плана, видимый снаружи запроса: спросить у
// площадки, каким планом она сходила в базу, нельзя, а подменять хранилище ради этого
// значило бы проверять не то, что выполняется в бою. База обновляет счётчики не сразу
// после запроса, поэтому ответа мы ждём, а не спрашиваем один раз.
func (p *platform) indexWasUsed(t *testing.T, index string) bool {
	t.Helper()

	deadline := time.Now().Add(indexStatsTimeout)
	for {
		var scans int64
		row := p.db.QueryRowContext(t.Context(), `
			SELECT coalesce(sum(idx_scan), 0)
			FROM pg_stat_user_indexes
			WHERE schemaname = 'platform' AND indexrelname = $1`, index)
		if err := row.Scan(&scans); err != nil {
			t.Fatalf("спросить об обращениях к индексу %s: %v", index, err)
		}
		if scans > 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}

		time.Sleep(indexStatsPoll)
	}
}

const (
	// catalogLargeEnoughToNeedAnIndex — сколько позиций кладём в каталог, чтобы перебор
	// таблицы перестал быть для планировщика самым дешёвым способом ответить.
	catalogLargeEnoughToNeedAnIndex = 50_000
	// indexStatsTimeout — сколько ждём, пока база сведёт счётчики обращений к индексам.
	indexStatsTimeout = 10 * time.Second
	// indexStatsPoll — как часто переспрашиваем.
	indexStatsPoll = 100 * time.Millisecond
)
