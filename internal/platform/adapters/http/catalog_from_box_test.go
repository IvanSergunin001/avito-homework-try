// Правку меню в коробке заведения, доехавшую до витрины площадки, заглушкой не проверить:
// заглушка отвечает то, что ей велел тест, и подтвердила бы только саму себя. Поэтому по
// ту сторону контракта здесь стоит настоящая коробка — тот же роутер со всеми middleware
// и та же база, что и в запуске, — а меню в ней ведёт оператор своей ручкой.
//
// Шов при этом остаётся прежним: обе службы разговаривают по HTTP, изнутри не подменено
// ничего. Заглушка нужна там, где заведение должно вести себя не по контракту, — этого от
// коробки не добиться, и именно поэтому она не заменяет заглушку в остальных тестах.
package http_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/pkg/logger"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
	boxdomain "github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// operatorBox — настоящая коробка заведения, поднятая рядом с площадкой: меню в ней ведёт
// оператор, а площадка забирает его по партнёрскому контракту.
type operatorBox struct {
	srv *httptest.Server
}

// openBox поднимает коробку на базе того же теста. База одна на обе службы, но схемы
// разные, и ни одного внешнего ключа между ними нет: граница контекстов физическая, и
// коробка не видит данных площадки, даже стоя с ней в одном инстансе.
func (p *platform) openBox(t *testing.T) *operatorBox {
	t.Helper()

	// Часы работы коробке здесь безразличны: тест забирает у неё меню, а не заказывает.
	srv := httptest.NewServer(restauranthttp.NewRouter(
		logger.New(io.Discard, slog.LevelInfo), p.db.DB, boxdomain.AllDay()))
	t.Cleanup(srv.Close)

	return &operatorBox{srv: srv}
}

// operatorItem — позиция меню глазами оператора коробки.
type operatorItem struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Price       int64     `json:"price"`
	InStopList  bool      `json:"in_stop_list"`
	IsAvailable bool      `json:"is_available"`
}

// addItem заводит позицию меню рабочим местом оператора и возвращает её.
func (b *operatorBox) addItem(t *testing.T, name string, price int64, stock int) operatorItem {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"price":%d,"stock":%d}`, name, price, stock)

	return b.item(t, b.do(t, http.MethodPost, "/api/operator/v1/menu", body), http.StatusCreated)
}

// editCard правит карточку позиции: название, описание и цену.
func (b *operatorBox) editCard(t *testing.T, id uuid.UUID, name string, price int64) operatorItem {
	t.Helper()

	body := fmt.Sprintf(`{"name":%q,"price":%d}`, name, price)

	return b.item(t, b.do(t, http.MethodPut, "/api/operator/v1/menu/"+id.String(), body), http.StatusOK)
}

// markStopList отмечает позицию как отсутствующую или снимает отметку.
func (b *operatorBox) markStopList(t *testing.T, id uuid.UUID, inStopList bool) operatorItem {
	t.Helper()

	body := fmt.Sprintf(`{"in_stop_list":%t}`, inStopList)

	return b.item(t, b.do(t, http.MethodPut,
		"/api/operator/v1/menu/"+id.String()+"/stop-list", body), http.StatusOK)
}

// removeItem убирает позицию из меню заведения.
func (b *operatorBox) removeItem(t *testing.T, id uuid.UUID) {
	t.Helper()

	if resp := b.do(t, http.MethodDelete, "/api/operator/v1/menu/"+id.String(), ""); resp.Status != http.StatusNoContent {
		t.Fatalf("удаление позиции вернуло %d, ожидали 204: %s", resp.Status, resp.Body)
	}
}

// do выполняет запрос к рабочему месту оператора коробки.
func (b *operatorBox) do(t *testing.T, method, path, body string) response {
	t.Helper()

	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, b.srv.URL+path, payload)
	if err != nil {
		t.Fatalf("собрать запрос %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("прочитать ответ %s %s: %v", method, path, err)
	}

	return response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: read}
}

// item разбирает ответ рабочего места как позицию меню.
func (b *operatorBox) item(t *testing.T, resp response, status int) operatorItem {
	t.Helper()

	if resp.Status != status {
		t.Fatalf("коробка ответила %d, ожидали %d: %s", resp.Status, status, resp.Body)
	}

	var item operatorItem
	if err := json.Unmarshal(resp.Body, &item); err != nil {
		t.Fatalf("разобрать позицию меню коробки: %v", err)
	}

	return item
}

func TestPriceEditedInTheBoxIsVisibleOnTheShowcaseAfterRefresh(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	croissant := box.addItem(t, "Круассан", 45000, 10)
	platform.refreshCatalog(t, registered.Key)

	if got := platform.products(t, menuPath(registered.ID, "")).item(t, "Круассан").Price; got != 45000 {
		t.Fatalf("на витрине цена %d, ожидали заведённую в коробке 45000", got)
	}

	box.editCard(t, croissant.ID, "Круассан с миндалём", 52000)
	platform.refreshCatalog(t, registered.Key)

	// Позиция та же самая: внешним идентификатором коробка отдаёт свой идентификатор
	// позиции, и правка карточки его не меняет.
	card := platform.products(t, menuPath(registered.ID, "")).item(t, "Круассан с миндалём")
	if card.Price != 52000 {
		t.Errorf("на витрине цена %d, ожидали поправленную оператором 52000", card.Price)
	}
	if card.ID != platformCardOf(t, platform, registered.ID, croissant.ID).String() {
		t.Error("после правки в коробке на витрине появилась вторая карточка вместо поправленной")
	}
}

func TestStopListMarkedInTheBoxReachesTheShowcaseAfterRefresh(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	croissant := box.addItem(t, "Круассан", 45000, 10)
	platform.refreshCatalog(t, registered.Key)

	box.markStopList(t, croissant.ID, true)
	platform.refreshCatalog(t, registered.Key)

	// Позиция остаётся на витрине: убери её — и покупатель искал бы блюдо, которое вчера
	// видел, не понимая, куда оно делось.
	card := platform.products(t, menuPath(registered.ID, "")).item(t, "Круассан")
	if card.IsAvailable {
		t.Error("позиция, отмеченная оператором в стоп-листе, на витрине доступна")
	}

	box.markStopList(t, croissant.ID, false)
	platform.refreshCatalog(t, registered.Key)

	if !platform.products(t, menuPath(registered.ID, "")).item(t, "Круассан").IsAvailable {
		t.Error("позиция, снятая оператором со стоп-листа, на витрине так и осталась недоступной")
	}
}

func TestItemRemovedInTheBoxDisappearsFromTheShowcaseAfterRefresh(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	box := platform.openBox(t)
	registered := platform.register(t, "Пекарня «Батон»", box.srv.URL)

	croissant := box.addItem(t, "Круассан", 45000, 10)
	baguette := box.addItem(t, "Багет", 12000, 5)
	platform.refreshCatalog(t, registered.Key)

	box.removeItem(t, baguette.ID)
	platform.refreshCatalog(t, registered.Key)

	names := platform.products(t, menuPath(registered.ID, "")).names()
	if len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали только оставшийся в коробке «Круассан»", names)
	}

	// Оставшаяся позиция не пострадала от чужого удаления: выгрузка применяется целиком,
	// а не «всё, что не упомянуто, долой».
	if stored, exists := platform.storedPosition(t, registered.ID, croissant.ID.String()); !exists || !stored.IsActive {
		t.Error("позиция, оставшаяся в меню коробки, ушла с витрины вместе с удалённой")
	}
}

// platformCardOf — идентификатор карточки, которой площадка знает позицию коробки: она
// узнаёт её по внешнему идентификатору, а внешним идентификатором коробка отдаёт свой
// идентификатор позиции.
func platformCardOf(t *testing.T, p *platform, restaurant, item uuid.UUID) uuid.UUID {
	t.Helper()

	stored, exists := p.storedPosition(t, restaurant, item.String())
	if !exists {
		t.Fatalf("площадка не знает позицию %s заведения %s", item, restaurant)
	}

	return stored.ID
}
