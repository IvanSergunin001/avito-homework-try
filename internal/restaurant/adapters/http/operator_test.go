package http_test

import (
	"net/http"
	"strconv"
	"testing"

	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// missingItem — идентификатор позиции, которой в меню нет: правильный uuid v7, просто
// ничей. Неправильный uuid проверял бы разбор параметра, а не правило предметной области.
const missingItem = "018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a09"

// operatorItemByID находит позицию на рабочем месте оператора по идентификатору.
func (b *box) operatorItemByID(t *testing.T, id string) operatorItem {
	t.Helper()

	for _, item := range b.operatorMenuPage(t, "").Items {
		if item.ID == id {
			return item
		}
	}

	t.Fatalf("позиции %s нет в меню оператора", id)

	return operatorItem{}
}

func TestItemAddedByOperatorAppearsInHisMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{
		Name:        "Круассан с миндалём",
		Description: "Слоёное тесто, миндальный крем",
		Price:       45000,
		Stock:       10,
	})

	item := b.operatorItemByID(t, added.ID)

	if item.Name != "Круассан с миндалём" || item.Price != 45000 || item.Stock != 10 {
		t.Errorf("в меню %q за %d при остатке %d, ожидали заведённое оператором",
			item.Name, item.Price, item.Stock)
	}
	if item.Description == nil || *item.Description != "Слоёное тесто, миндальный крем" {
		t.Errorf("описание %v, ожидали заведённое оператором", item.Description)
	}
}

func TestItemWithoutDescriptionHasNoDescriptionAtAll(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Компот", Price: 12000, Stock: 20})

	if added.Description != nil {
		t.Errorf("описание пришло как %q, ожидали отсутствие поля: пустая строка сообщила "+
			"бы, что описание есть и оно пустое", *added.Description)
	}
}

func TestNameOfItemIsKeptWithoutTheSpacesTheOperatorTypedAround(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "  Сырники  ", Price: 25000, Stock: 4})

	if added.Name != "Сырники" {
		t.Errorf("название сохранено как %q, ожидали «Сырники»: пробелы по краям — след "+
			"ввода, а не часть названия", added.Name)
	}
}

func TestEditReplacesTheWholeCardOfTheItem(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Эклер", Description: "С кремом", Price: 30000, Stock: 3})

	var edited operatorItem
	decode(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID,
		map[string]any{"name": "Эклер шоколадный", "price": 35000}), http.StatusOK, &edited)

	if edited.Name != "Эклер шоколадный" || edited.Price != 35000 {
		t.Errorf("после правки %q за %d, ожидали присланное оператором", edited.Name, edited.Price)
	}
	if edited.Description != nil {
		t.Errorf("описание осталось %q, ожидали, что его не станет: правка заменяет карточку "+
			"целиком, а не по полям", *edited.Description)
	}
}

func TestEditOfTheCardTouchesNeitherStockNorStopListMark(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Устрицы", Price: 90000, Stock: 12})
	b.markStopList(t, added.ID, true)

	var edited operatorItem
	decode(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID,
		map[string]any{"name": "Устрицы Фин де Клер", "price": 95000}), http.StatusOK, &edited)

	if edited.Stock != 12 {
		t.Errorf("после правки карточки остаток %d, ожидали прежние 12", edited.Stock)
	}
	if !edited.InStopList {
		t.Error("правка карточки сняла отметку стоп-листа: цену и наличие оператор ведёт порознь")
	}
}

func TestOperatorSeesWhichOfTheTwoReasonsMakesItemUnavailable(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	stopped := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})
	empty := b.addItem(t, newItem{Name: "Ватрушка", Price: 8000, Stock: 0})

	marked := b.markStopList(t, stopped.ID, true)

	if !marked.InStopList || marked.Stock != 5 || marked.IsAvailable {
		t.Errorf("позиция в стоп-листе: отметка=%v остаток=%d доступна=%v, ожидали true, 5 и false",
			marked.InStopList, marked.Stock, marked.IsAvailable)
	}
	if empty.InStopList || empty.Stock != 0 || empty.IsAvailable {
		t.Errorf("позиция без остатка: отметка=%v остаток=%d доступна=%v, ожидали false, 0 и false",
			empty.InStopList, empty.Stock, empty.IsAvailable)
	}
}

func TestRepeatedStopListMarkLeavesTheItemAsItWas(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})

	first := b.markStopList(t, added.ID, true)
	second := b.markStopList(t, added.ID, true)

	if first != second {
		t.Errorf("повторная отметка дала %+v вместо %+v: состояние у позиции одно, и "+
			"нажавший дважды оператор не должен получать другой ответ", second, first)
	}
}

func TestStockKeptByOperatorIsWrittenAsToldAndNotSubtracted(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 10})

	b.setStock(t, added.ID, 4)
	repeated := b.setStock(t, added.ID, 4)

	if repeated.Stock != 4 {
		t.Errorf("остаток стал %d, ожидали 4: оператор сообщает, сколько осталось, а не "+
			"сколько списать, — повтор запроса не должен списать порцию дважды", repeated.Stock)
	}
}

func TestItemRemovedByOperatorIsGoneFromHisMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Сезонный суп", Price: 30000, Stock: 4})

	if resp := b.send(t, http.MethodDelete, operatorMenu+"/"+added.ID, nil); resp.Status != http.StatusNoContent {
		t.Fatalf("удаление позиции вернуло %d, ожидали 204: %s", resp.Status, resp.Body)
	}

	if page := b.operatorMenuPage(t, ""); len(page.Items) != 0 || page.Page.Total != 0 {
		t.Errorf("после удаления в меню %d позиций при total=%d, ожидали пустое меню",
			len(page.Items), page.Page.Total)
	}
}

func TestOperatorMenuIsPagedWithTotalOfTheWholeMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	for i := range 3 {
		b.addItem(t, newItem{Name: "Позиция " + strconv.Itoa(i), Price: 10000, Stock: 1})
	}

	page := b.operatorMenuPage(t, "?limit=2&offset=2")

	if len(page.Items) != 1 {
		t.Errorf("последняя страница вернула %d позиций, ожидали одну", len(page.Items))
	}
	if page.Page.Total != 3 {
		t.Errorf("total страницы %d, ожидали 3: это размер меню, а не окна", page.Page.Total)
	}
}

func TestOperatorMenuUsesDeclaredWindowAndRefusesLargerOne(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	page := b.operatorMenuPage(t, "")
	if page.Page.Limit != restauranthttp.OperatorMenuPageDefault || page.Page.Offset != 0 {
		t.Errorf("страница limit=%d offset=%d, ожидали умолчания %d и 0",
			page.Page.Limit, page.Page.Offset, restauranthttp.OperatorMenuPageDefault)
	}

	overMax := strconv.Itoa(restauranthttp.OperatorMenuPageMax + 1)
	document := asRefusal(t, b.get(t, operatorMenu+"?limit="+overMax), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

func TestItemWithoutNameDoesNotGetIntoTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.send(t, http.MethodPost, operatorMenu,
		map[string]any{"name": "   ", "price": 10000, "stock": 1}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if page := b.operatorMenuPage(t, ""); page.Page.Total != 0 {
		t.Errorf("в меню %d позиций, ожидали ноль: отклонённая позиция не заводится", page.Page.Total)
	}
}

func TestItemWithNegativePriceDoesNotGetIntoTheMenu(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodPost, operatorMenu,
		map[string]any{"name": "Ошибка ввода", "price": -1, "stock": 1}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

func TestStockOfItemCannotBeNegative(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 2})

	document := asRefusal(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID+"/stock",
		map[string]any{"stock": -1}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if item := b.operatorItemByID(t, added.ID); item.Stock != 2 {
		t.Errorf("остаток стал %d, ожидали прежние 2: отклонённая правка ничего не меняет", item.Stock)
	}
}

func TestEditingItemThatIsNotInTheMenuIsRefusedAsNotFound(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodPut, operatorMenu+"/"+missingItem,
		map[string]any{"name": "Призрак", "price": 1000}), http.StatusNotFound)

	if document.Code != "menu_item_not_found" {
		t.Errorf("код отказа %q, ожидали menu_item_not_found", document.Code)
	}
}

func TestMarkingItemThatIsNotInTheMenuIsRefusedAsNotFound(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodPut, operatorMenu+"/"+missingItem+"/stop-list",
		map[string]any{"in_stop_list": true}), http.StatusNotFound)

	if document.Code != "menu_item_not_found" {
		t.Errorf("код отказа %q, ожидали menu_item_not_found", document.Code)
	}
}

func TestSettingStockOfItemThatIsNotInTheMenuIsRefusedAsNotFound(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodPut, operatorMenu+"/"+missingItem+"/stock",
		map[string]any{"stock": 5}), http.StatusNotFound)

	if document.Code != "menu_item_not_found" {
		t.Errorf("код отказа %q, ожидали menu_item_not_found", document.Code)
	}
}

func TestRemovingItemThatIsNotInTheMenuIsRefusedAsNotFound(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodDelete, operatorMenu+"/"+missingItem, nil),
		http.StatusNotFound)

	if document.Code != "menu_item_not_found" {
		t.Errorf("код отказа %q, ожидали menu_item_not_found", document.Code)
	}
}

func TestItemIdentifierThatIsNotAnIdentifierIsRefusedAsInvalidRequest(t *testing.T) {
	t.Parallel()

	document := asRefusal(t, newBox(t).send(t, http.MethodPut, operatorMenu+"/не-идентификатор/stock",
		map[string]any{"stock": 5}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

func TestBodyThatIsNotJSONIsRefusedAsInvalidRequest(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.do(t, http.MethodPost, operatorMenu, []byte("не json")), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

func TestItemWithoutPriceDoesNotGetIntoTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.send(t, http.MethodPost, operatorMenu,
		map[string]any{"name": "Забыли цену", "stock": 1}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed: позиция без цены — это не "+
			"бесплатная позиция, а незаполненная форма", document.Code)
	}
	if page := b.operatorMenuPage(t, ""); page.Page.Total != 0 {
		t.Errorf("в меню %d позиций, ожидали ноль", page.Page.Total)
	}
}

func TestStopListMarkWithoutTheMarkItselfIsRefused(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})
	b.markStopList(t, added.ID, true)

	document := asRefusal(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID+"/stop-list",
		map[string]any{}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if item := b.operatorItemByID(t, added.ID); !item.InStopList {
		t.Error("позиция снялась со стопа запросом без отметки: пустое тело не значит «есть в наличии»")
	}
}

func TestStopListMarkSentAsNullDoesNotTakeTheItemOffTheStopList(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Борщ", Price: 40000, Stock: 5})
	b.markStopList(t, added.ID, true)

	document := asRefusal(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID+"/stop-list",
		map[string]any{"in_stop_list": nil}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if item := b.operatorItemByID(t, added.ID); !item.InStopList {
		t.Error("позиция снялась со стопа отметкой null: «поле прислали пустым» — это то же " +
			"незаполненное поле, а не «есть в наличии»")
	}
}

func TestStockSentAsNullDoesNotMakeTheItemUnavailable(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 7})

	document := asRefusal(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID+"/stock",
		map[string]any{"stock": nil}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if item := b.operatorItemByID(t, added.ID); item.Stock != 7 {
		t.Errorf("остаток стал %d, ожидали прежние 7: остаток null обнулил бы позицию молча",
			item.Stock)
	}
}

func TestItemWithPriceSentAsNullDoesNotGetIntoTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.send(t, http.MethodPost, operatorMenu,
		map[string]any{"name": "Стёртая цена", "price": nil, "stock": 1}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed: цена null — это не бесплатная "+
			"позиция, а незаполненная форма", document.Code)
	}
	if page := b.operatorMenuPage(t, ""); page.Page.Total != 0 {
		t.Errorf("в меню %d позиций, ожидали ноль", page.Page.Total)
	}
}

func TestStockLargerThanTheBoxCanKeepIsRefusedAsInvalidRequest(t *testing.T) {
	t.Parallel()

	b := newBox(t)
	added := b.addItem(t, newItem{Name: "Пирожок", Price: 5000, Stock: 2})

	document := asRefusal(t, b.send(t, http.MethodPut, operatorMenu+"/"+added.ID+"/stock",
		map[string]any{"stock": 3_000_000_000}), http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed: опечатка оператора — это "+
			"неверный запрос, а не отказ службы, который он должен повторить позже", document.Code)
	}
	if item := b.operatorItemByID(t, added.ID); item.Stock != 2 {
		t.Errorf("остаток стал %d, ожидали прежние 2: отклонённая правка ничего не меняет", item.Stock)
	}
}

func TestItemWithStockLargerThanTheBoxCanKeepDoesNotGetIntoTheMenu(t *testing.T) {
	t.Parallel()

	b := newBox(t)

	document := asRefusal(t, b.send(t, http.MethodPost, operatorMenu,
		map[string]any{"name": "Опечатка в остатке", "price": 10000, "stock": 3_000_000_000}),
		http.StatusBadRequest)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
	if page := b.operatorMenuPage(t, ""); page.Page.Total != 0 {
		t.Errorf("в меню %d позиций, ожидали ноль", page.Page.Total)
	}
}
