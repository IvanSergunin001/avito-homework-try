package http_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// pageOfProducts — страница выдачи товаров глазами клиента: разбираем её так же, как
// разбирал бы покупательский фронтенд, — по схеме из спецификации, а не по внутренним
// типам площадки.
type pageOfProducts struct {
	Items []productCard `json:"items"`
	Page  struct {
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
		Total  int64 `json:"total"`
	} `json:"page"`
}

// productCard — товар глазами клиента.
type productCard struct {
	ID           string  `json:"id"`
	RestaurantID string  `json:"restaurant_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Price        int64   `json:"price"`
	IsAvailable  bool    `json:"is_available"`
}

// names — названия товаров страницы в том порядке, в каком их отдала площадка.
func (p pageOfProducts) names() []string {
	names := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		names = append(names, item.Name)
	}

	return names
}

// products запрашивает страницу выдачи товаров и разбирает её.
func (p *platform) products(t *testing.T, path string) pageOfProducts {
	t.Helper()

	resp := p.get(t, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s вернул %d, ожидали 200: %s", path, resp.Status, resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("тип содержимого %q, ожидали application/json", resp.ContentType)
	}

	var page pageOfProducts
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("разобрать страницу товаров: %v", err)
	}

	return page
}

// product запрашивает карточку товара и разбирает её.
func (p *platform) product(t *testing.T, id uuid.UUID) productCard {
	t.Helper()

	path := "/api/v1/products/" + id.String()

	resp := p.get(t, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s вернул %d, ожидали 200: %s", path, resp.Status, resp.Body)
	}

	var card productCard
	if err := json.Unmarshal(resp.Body, &card); err != nil {
		t.Fatalf("разобрать карточку товара: %v", err)
	}

	return card
}

// menuPath — адрес меню заведения с параметрами выдачи.
func menuPath(restaurant uuid.UUID, query string) string {
	path := "/api/v1/restaurants/" + restaurant.String() + "/products"
	if query != "" {
		path += "?" + query
	}

	return path
}

func TestMenuIsGivenPageByPageWithTheWindowDeclaredInContractWhenClientDoesNotAskForOne(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.openRestaurant(t, "Пекарня «Батон»")
	platform.addProduct(t, restaurant, menuItem{Name: "Круассан", Price: 12000})

	page := platform.products(t, menuPath(restaurant, ""))

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

	var seen []string
	for _, offset := range []string{"0", "2", "4"} {
		page := platform.products(t, menuPath(restaurant, "limit=2&offset="+offset))
		if page.Page.Total != 5 {
			t.Errorf("offset=%s: total=%d, ожидали 5 — общее число не зависит от окна", offset, page.Page.Total)
		}
		if len(page.Items) > 2 {
			t.Errorf("offset=%s: на странице %d позиций, а просили не больше двух", offset, len(page.Items))
		}
		seen = append(seen, page.names()...)
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

	document := platform.refusal(t, menuPath(restaurant, "limit=101"), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Detail == "" {
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

	page := platform.products(t, menuPath(bakery, ""))

	if got := page.names(); !slices.Equal(got, []string{"Круассан"}) {
		t.Errorf("в меню пекарни %v, ожидали только её «Круассан»", got)
	}
}

func TestMenuOfRestaurantThatStoppedWorkingIsNotFoundInsteadOfBeingEmpty(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.closedRestaurant(t, "Столовая «Обед»")
	platform.addProduct(t, restaurant, menuItem{Name: "Компот", Price: 5000})

	document := platform.refusal(t, menuPath(restaurant, ""), http.StatusNotFound)

	if document.Code != "restaurant_not_found" {
		t.Errorf("код отказа %q, ожидали restaurant_not_found", document.Code)
	}
}

func TestMenuOfRestaurantThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		menuPath(uuid.MustParse("018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a01"), ""), http.StatusNotFound)

	if document.Code != "restaurant_not_found" {
		t.Errorf("код отказа %q, ожидали restaurant_not_found", document.Code)
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

	if card.ID != id.String() || card.RestaurantID != restaurant.String() {
		t.Errorf("карточка отдала товар %q заведения %q, ожидали %q и %q",
			card.ID, card.RestaurantID, id, restaurant)
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
	if err := json.Unmarshal(platform.get(t, "/api/v1/products/"+id.String()).Body, &fields); err != nil {
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

	page := platform.products(t, menuPath(restaurant, ""))
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

	document := newPlatform(t).refusal(t,
		"/api/v1/products/018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a02", http.StatusNotFound)

	if document.Code != "product_not_found" {
		t.Errorf("код отказа %q, ожидали product_not_found", document.Code)
	}
}

func TestCardOfProductOfRestaurantThatStoppedWorkingIsNotFoundEither(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	restaurant := platform.closedRestaurant(t, "Столовая «Обед»")
	id := platform.addProduct(t, restaurant, menuItem{Name: "Компот", Price: 5000})

	document := platform.refusal(t, "/api/v1/products/"+id.String(), http.StatusNotFound)

	if document.Code != "product_not_found" {
		t.Errorf("код отказа %q, ожидали product_not_found", document.Code)
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

	page := platform.products(t, "/api/v1/products?q=круассан")

	got := page.names()
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

	page := platform.products(t, menuPath(bakery, "q=круассан"))

	if got := page.names(); !slices.Equal(got, []string{"Круассан с миндалём"}) {
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

	for _, query := range []string{"МИНДАЛ", "миндал", "уассан с"} {
		page := platform.products(t, "/api/v1/products?q="+url.QueryEscape(query))
		if got := page.names(); !slices.Equal(got, []string{"Круассан с миндалём"}) {
			t.Errorf("по запросу %q нашлось %v, ожидали «Круассан с миндалём»", query, got)
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

	page := platform.products(t, "/api/v1/products?q=круассан")

	if got := page.names(); !slices.Equal(got, []string{"Круассан с миндалём"}) {
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

	page := platform.products(t, "/api/v1/products?q="+url.QueryEscape("50%"))

	if got := page.names(); !slices.Equal(got, []string{"Набор 50% миндаля"}) {
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

	for _, path := range []string{"/api/v1/products", "/api/v1/products?q="} {
		page := platform.products(t, path)

		got := page.names()
		slices.Sort(got)
		if !slices.Equal(got, []string{"Капучино", "Круассан"}) {
			t.Errorf("%s отдал %v, ожидали весь каталог: пустой запрос — это отсутствие запроса", path, got)
		}
	}
}

func TestSearchRefusesPageLargerThanContractAllows(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t, "/api/v1/products?limit=101", http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if document.Status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", document.Status)
	}
}

func TestSearchRefusesQueryLongerThanContractAllows(t *testing.T) {
	t.Parallel()

	document := newPlatform(t).refusal(t,
		"/api/v1/products?q="+url.QueryEscape(strings.Repeat("к", 201)), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
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

	page := platform.products(t, "/api/v1/products?q=круассан")
	if got := page.names(); !slices.Equal(got, []string{"Круассан с миндалём"}) {
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
