package http_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	partner "github.com/IvanSergunin001/Test157/api/partner/v1"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// platformSide — сгенерированный по партнёрскому контракту клиент, которым в коробку
// ходит площадка. Тест проверяет коробку тем же клиентом, что и синхронизация каталога:
// разойтись с контрактом такой тест не может.
func (b *box) platformSide(t *testing.T) *partner.ClientWithResponses {
	t.Helper()

	client, err := partner.NewClientWithResponses(b.srv.URL+restauranthttp.PartnerAPIPrefix,
		partner.WithHTTPClient(b.srv.Client()))
	if err != nil {
		t.Fatalf("собрать клиент партнёрского контракта: %v", err)
	}

	return client
}

// menuGivenToPlatform читает страницу меню так, как её читала бы площадка.
func (b *box) menuGivenToPlatform(t *testing.T, params *partner.ListMenuParams) partner.MenuPage {
	t.Helper()

	resp, err := b.platformSide(t).ListMenuWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("запросить меню: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("меню вернуло %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	return *resp.JSON200
}

// itemGivenToPlatform находит позицию в партнёрской выдаче по внешнему идентификатору.
func (b *box) itemGivenToPlatform(t *testing.T, externalID string) partner.MenuItem {
	t.Helper()

	for _, item := range b.menuGivenToPlatform(t, &partner.ListMenuParams{}).Items {
		if item.ExternalId == externalID {
			return item
		}
	}

	t.Fatalf("позиции %s нет в меню, отданном площадке", externalID)

	return partner.MenuItem{}
}

func TestMenuPageUsesWindowDeclaredInContractWhenPlatformDoesNotAskForOne(t *testing.T) {
	t.Parallel()

	page := newBox(t).menuGivenToPlatform(t, &partner.ListMenuParams{})

	if page.Page.Limit != restauranthttp.MenuPageDefault || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания контракта %d и 0",
			page.Page.Limit, page.Page.Offset, restauranthttp.MenuPageDefault)
	}
}

func TestMenuPageEchoesTheWindowThePlatformAskedFor(t *testing.T) {
	t.Parallel()

	limit, offset := restauranthttp.MenuPageMax, 1000

	page := newBox(t).menuGivenToPlatform(t, &partner.ListMenuParams{Limit: &limit, Offset: &offset})

	if page.Page.Limit != limit || page.Page.Offset != offset {
		t.Errorf("страница limit=%d offset=%d, ожидали запрошенные %d и %d",
			page.Page.Limit, page.Page.Offset, limit, offset)
	}
}

func TestEmptyMenuIsAnEmptyPageAndNotAnAbsentCollection(t *testing.T) {
	t.Parallel()

	page := newBox(t).menuGivenToPlatform(t, &partner.ListMenuParams{})

	if page.Items == nil {
		t.Error("items пришли как null, ожидали пустой массив: контракт обещает коллекцию всегда")
	}
	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("в пустом меню %d позиций при total=%d, ожидали ноль и ноль",
			len(page.Items), page.Page.Total)
	}
}

func TestMenuRefusesPageLargerThanContractAllows(t *testing.T) {
	t.Parallel()

	limit := restauranthttp.MenuPageMax + 1

	resp, err := newBox(t).platformSide(t).ListMenuWithResponse(context.Background(),
		&partner.ListMenuParams{Limit: &limit})
	if err != nil {
		t.Fatalf("запросить меню: %v", err)
	}
	if resp.ApplicationproblemJSON400 == nil {
		t.Fatalf("меню вернуло %d, ожидали 400: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if code := resp.ApplicationproblemJSON400.Code; code != partner.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", code, partner.ValidationFailed)
	}
	if status := resp.ApplicationproblemJSON400.Status; status != http.StatusBadRequest {
		t.Errorf("в теле отказа status=%d, ожидали 400", status)
	}
}

// Нечисловой `limit` клиентом не отправить — его тип в контракте целое, — поэтому такой
// запрос идёт сырым: он проверяет разбор параметров, который живёт до обработчика.
func TestMenuRefusesPageSizeThatIsNotANumber(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).get(t, contractMenu+"?limit=неполное"), http.StatusBadRequest)

	if document.Code != string(partner.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, partner.ValidationFailed)
	}
}

func TestItemKeptByOperatorIsWhatThePlatformSees(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{
		Name:        "Круассан с миндалём",
		Description: "Слоёное тесто, миндальный крем",
		Price:       45000,
		Stock:       10,
	})

	item := b.itemGivenToPlatform(t, added.ID)

	if item.Name != "Круассан с миндалём" {
		t.Errorf("название %q, ожидали заведённое оператором", item.Name)
	}
	if item.Description == nil || *item.Description != "Слоёное тесто, миндальный крем" {
		t.Errorf("описание %v, ожидали заведённое оператором", item.Description)
	}
	if !item.IsAvailable {
		t.Error("позиция без отметки стоп-листа и с остатком отдана недоступной")
	}
}

func TestExternalIdentifierOfItemIsTheOneTheBoxIssuedWhenItWasAdded(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Эклер", Price: 30000, Stock: 3})

	if added.ID == "" {
		t.Fatal("коробка не выдала позиции идентификатор")
	}
	if item := b.itemGivenToPlatform(t, added.ID); item.ExternalId != added.ID {
		t.Errorf("внешний идентификатор %q, ожидали выданный коробкой %q", item.ExternalId, added.ID)
	}
}

func TestExternalIdentifierOfItemSurvivesEditingOfItsCard(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Эклер", Price: 30000, Stock: 3})

	var edited operatorItem
	decode(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID,
		map[string]any{"name": "Эклер шоколадный", "price": 35000}), http.StatusOK, &edited)

	item := b.itemGivenToPlatform(t, added.ID)

	if item.ExternalId != added.ID {
		t.Errorf("после правки внешний идентификатор %q, ожидали прежний %q: площадка "+
			"обновляет каталог по нему, и смена завела бы на витрине вторую позицию",
			item.ExternalId, added.ID)
	}
	if item.Name != "Эклер шоколадный" || item.Price != 35000 {
		t.Errorf("площадка видит %q за %d, ожидали исправленные оператором", item.Name, item.Price)
	}
}

func TestPriceReachesThePlatformInTheSameMinorUnitsItWasKeptIn(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	// Цена, которую дробное число денег испортило бы: 1234,57 рубля.
	added := b.addItem(t, newItem{Name: "Торт на заказ", Price: 123457, Stock: 1})

	if item := b.itemGivenToPlatform(t, added.ID); item.Price != 123457 {
		t.Errorf("цена уехала площадке как %d, ожидали 123457 копеек ровно", item.Price)
	}
}

func TestItemMarkedByOperatorAsMissingIsGivenToThePlatformAsUnavailable(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})

	b.markStopList(t, added.ID, true)

	item := b.itemGivenToPlatform(t, added.ID)

	if item.IsAvailable {
		t.Error("позиция в стоп-листе отдана площадке доступной")
	}
	if item.Name != "Борщ" {
		t.Errorf("позиции в стоп-листе не нашлось в меню (%q): она остаётся в выдаче, "+
			"иначе покупатель искал бы блюдо, которое вчера видел", item.Name)
	}
}

func TestItemWithoutStockIsGivenToThePlatformAsUnavailable(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 2})

	b.setStock(t, added.ID, 0)

	if item := b.itemGivenToPlatform(t, added.ID); item.IsAvailable {
		t.Error("позиция с нулевым остатком отдана площадке доступной")
	}
}

func TestItemReturnsToAvailabilityWhenOperatorTakesOffTheStopListMark(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})
	b.markStopList(t, added.ID, true)

	b.markStopList(t, added.ID, false)

	if item := b.itemGivenToPlatform(t, added.ID); !item.IsAvailable {
		t.Error("позиция после снятия отметки осталась недоступной, хотя остаток есть")
	}
}

func TestItemReturnsToAvailabilityWhenOperatorReplenishesStock(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 0})

	b.setStock(t, added.ID, 7)

	if item := b.itemGivenToPlatform(t, added.ID); !item.IsAvailable {
		t.Error("позиция с пополненным остатком осталась недоступной")
	}
}

func TestReplenishedStockDoesNotTakeItemOutOfTheStopList(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Устрицы", Price: 90000, Stock: 0})
	b.markStopList(t, added.ID, true)

	b.setStock(t, added.ID, 12)

	if item := b.itemGivenToPlatform(t, added.ID); item.IsAvailable {
		t.Error("позиция с остатком стала доступной, хотя оператор держит её в стоп-листе: " +
			"снять позицию со стопа может только он")
	}
}

func TestItemRemovedByOperatorDisappearsFromTheMenuGivenToThePlatform(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Сезонный суп", Price: 30000, Stock: 4})

	if resp := b.send(t, http.MethodDelete, operatorMenu+"/"+added.ID, nil); resp.Status != http.StatusNoContent {
		t.Fatalf("удаление позиции вернуло %d, ожидали 204: %s", resp.Status, resp.Body)
	}

	page := b.menuGivenToPlatform(t, &partner.ListMenuParams{})

	if len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("после удаления площадке отдано %d позиций при total=%d, ожидали пустое меню",
			len(page.Items), page.Page.Total)
	}
}

func TestMenuIsGivenToThePlatformPageByPageWithTotalOfTheWholeMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	for _, name := range []string{"Борщ", "Ватрушка", "Голубцы"} {
		b.addItem(t, newItem{Name: name, Price: 10000, Stock: 1})
	}

	limit, offset := 2, 2

	first := b.menuGivenToPlatform(t, &partner.ListMenuParams{Limit: &limit})
	last := b.menuGivenToPlatform(t, &partner.ListMenuParams{Limit: &limit, Offset: &offset})

	if len(first.Items) != 2 || len(last.Items) != 1 {
		t.Errorf("страницы вышли по %d и %d позиций, ожидали 2 и 1", len(first.Items), len(last.Items))
	}
	if first.Page.Total != 3 || last.Page.Total != 3 {
		t.Errorf("total страниц %d и %d, ожидали 3 на обеих: это размер меню, а не окна",
			first.Page.Total, last.Page.Total)
	}
}

func TestEveryItemReachesThePlatformExactlyOnceWhenMenuIsWalkedPageByPage(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	// Одинаковые названия — тот случай, ради которого порядок выдачи включает второй
	// ключ: без него одноимённые позиции переставлялись бы между запросами, и площадка
	// то читала бы одну дважды, то теряла её из каталога.
	for range 3 {
		b.addItem(t, newItem{Name: "Комплимент от шефа", Price: 0, Stock: 1})
	}

	limit := 1

	seen := map[string]int{}
	for offset := range 3 {
		page := b.menuGivenToPlatform(t, &partner.ListMenuParams{Limit: &limit, Offset: &offset})
		if len(page.Items) != 1 {
			t.Fatalf("страница со смещением %d вернула %d позиций, ожидали одну", offset, len(page.Items))
		}
		seen[page.Items[0].ExternalId]++
	}

	if len(seen) != 3 {
		t.Errorf("обход меню страницами дал %d различных позиций из 3: %v", len(seen), seen)
	}
}

// Предел на тело стоит и у коробки: заказ приносит ей площадка, и тело неограниченного
// размера читается в память до того, как хоть одно правило успеет сказать, что запрос
// неверен.
func TestRequestBodyLargerThanTheLimitIsRefusedByTheBox(t *testing.T) {
	t.Parallel()

	oversized := bytes.Repeat([]byte("a"), 2*httpx.MB)

	document := asRefusal(t, newBox(t).do(t, http.MethodPost,
		restauranthttp.PartnerAPIPrefix+"/orders", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != string(partner.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, partner.ValidationFailed)
	}
	if document.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("в теле отказа status=%d, ожидали 413", document.Status)
	}
}
