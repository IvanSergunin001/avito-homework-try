// Синхронизация каталога проверяется через тот же шов, что и всё остальное: настоящий
// роутер площадки, настоящий PostgreSQL, заведение — HTTP-заглушка, отвечающая по
// партнёрскому контракту. Своего шва у синхронизации нет, и заводить его незачем.
//
// Запускается она партнёрской ручкой внеочередного обновления, а не ожиданием тика
// планировщика: планировщик — тонкая обёртка над тем же сценарием, и тест, который спит в
// надежде на срабатывание таймера, проверяет не правило площадки, а планировщик Go.
package http_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	customer "github.com/IvanSergunin001/Test157/api/customer/v1"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// boxItem — позиция меню заведения-заглушки. Нули полей — обычная позиция: без описания,
// бесплатная и в наличии, — поэтому тест называет только то, что проверяет.
type boxItem struct {
	// ExternalID — идентификатор позиции в системе заведения: по паре «заведение + он»
	// площадка узнаёт позицию между выгрузками.
	ExternalID string
	// Name — название позиции, как его увидит покупатель.
	Name string
	// Description — описание позиции; пустое означает, что заведение его не дало.
	Description string
	// Price — цена в копейках.
	Price int64
	// OutOfStock — позиция в стоп-листе заведения.
	OutOfStock bool
}

// box — заведение по ту сторону партнёрского контракта.
//
// Это заглушка, а не подменённый интерфейс клиента: шов площадки проходит по границе
// протокола, и подменять исходящий вызов моком значило бы проверять, что площадка зовёт
// нужный метод, вместо того чтобы проверять, что она разговаривает по контракту.
type box struct {
	srv *httptest.Server

	mu sync.Mutex
	// menu — меню, которое заведение отдаёт площадке.
	menu []boxItem
	// answer — подменённый ответ на любой запрос площадки; nil означает обычное поведение
	// по контракту. Им тесты изображают сломанное заведение: отказ посреди выгрузки,
	// ответ не по контракту, молчание.
	answer http.HandlerFunc
	// visits — сколько раз площадка приходила к заведению.
	visits int
	// deliveries — заказы, которые площадка передала заведению, вместе с заголовками
	// контракта. Заглушка запоминает их, чтобы тест смотрел на то, что уехало по
	// протоколу, а не на то, что площадка собиралась отправить.
	deliveries []delivery
	// watch — чем заглушка пользуется, пока держит вызов площадки: заглянуть в её
	// хранилище. Правило «заказ записан до обращения к заведению» иначе не проверить —
	// снаружи оба состояния видны уже после ответа.
	watch func(order uuid.UUID) string
}

// newBox поднимает заведение с пустым меню.
func newBox(t *testing.T) *box {
	t.Helper()

	b := &box{}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)

	return b
}

// handle отвечает на запрос площадки той половиной партнёрского контракта, которую
// обязано реализовать заведение: отдаёт страницу меню и принимает переданный заказ.
// Подменённый ответ, если он задан, перекрывает и то и другое: сломанное заведение ломается
// на всём.
func (b *box) handle(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.visits++
	answer := b.answer
	b.mu.Unlock()

	if answer != nil {
		answer(w, r)

		return
	}

	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/orders") {
		b.acceptOrder(w, r)

		return
	}

	b.writeMenuPage(w, r)
}

// setMenu заменяет меню заведения целиком — так же, как его меняет оператор в коробке.
func (b *box) setMenu(items ...boxItem) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.menu = items
}

// answerWith заставляет заведение отвечать на запрос меню чем-то, кроме обычной выдачи.
func (b *box) answerWith(answer http.HandlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.answer = answer
}

// visited сообщает, сколько раз площадка приходила за меню.
func (b *box) visited() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.visits
}

// writeMenuPage отдаёт страницу меню по партнёрскому контракту.
func (b *box) writeMenuPage(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r, "limit", 100)
	offset := intQuery(r, "offset", 0)

	b.mu.Lock()
	menu := append([]boxItem(nil), b.menu...)
	b.mu.Unlock()

	items := make([]menuItemDocument, 0, limit)
	for index := offset; index < len(menu) && len(items) < limit; index++ {
		items = append(items, contractItem(menu[index]))
	}

	writeMenu(w, menuPageDocument{
		Items: items,
		Page:  pageMetaDocument{Limit: limit, Offset: offset, Total: int64(len(menu))},
	})
}

// menuPageDocument, menuItemDocument и pageMetaDocument — страница меню в схеме
// партнёрского контракта: заглушка отвечает именно ею, а не внутренними типами площадки,
// иначе тест проверял бы разговор площадки с самой собой.
type menuPageDocument struct {
	Items []menuItemDocument `json:"items"`
	Page  pageMetaDocument   `json:"page"`
}

type menuItemDocument struct {
	ExternalID  string  `json:"external_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Price       int64   `json:"price"`
	IsAvailable bool    `json:"is_available"`
}

type pageMetaDocument struct {
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// contractItem переводит позицию заглушки в схему контракта.
func contractItem(item boxItem) menuItemDocument {
	document := menuItemDocument{
		ExternalID:  item.ExternalID,
		Name:        item.Name,
		Price:       item.Price,
		IsAvailable: !item.OutOfStock,
	}
	if item.Description != "" {
		document.Description = &item.Description
	}

	return document
}

// writeMenu отправляет страницу меню заведения.
func writeMenu(w http.ResponseWriter, page menuPageDocument) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// intQuery читает числовой параметр запроса.
func intQuery(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}

	return value
}

// connect подключает к площадке заведение, живущее в заглушке: адрес интеграции — адрес
// поднятого заведения, а не выдуманный, поэтому площадке есть куда идти за меню.
func (p *platform) connect(t *testing.T, name string, restaurant *box) registration {
	t.Helper()

	return p.register(t, name, restaurant.srv.URL)
}

// refreshCatalog просит площадку забрать меню заведения прямо сейчас — партнёрской ручкой
// внеочередного обновления, ключом самого заведения.
//
// Ответ всегда `202`: заведение просит площадку сходить за меню, а не рассказать, что она
// о нём думает. Что синхронизация не удалась, видно не по коду ответа, а по каталогу,
// который остался прежним, — так это увидит и заведение.
func (p *platform) refreshCatalog(t *testing.T, key string) {
	t.Helper()

	resp := p.do(t, http.MethodPost, catalogRefresh, key, nil)
	if resp.Status != http.StatusAccepted {
		t.Fatalf("внеочередное обновление вернуло %d, ожидали 202: %s", resp.Status, resp.Body)
	}
}

// storedPosition — позиция каталога площадки, какой её видно в хранилище.
//
// Снаружи деактивированной позиции не видно вовсе — в этом и смысл деактивации, — а
// правило «исчезнувшая позиция деактивируется, а не удаляется» проверяется именно по
// хранимой строке: удалённая унесла бы с собой историю и ссылку, которая уже у покупателя.
type storedPosition struct {
	ID       uuid.UUID
	Name     string
	Price    int64
	IsActive bool
}

// storedPosition читает позицию каталога по паре «заведение + внешний идентификатор» —
// по тому самому ключу, по которому её обновляет синхронизация.
func (p *platform) storedPosition(t *testing.T, restaurant uuid.UUID, externalID string) (storedPosition, bool) {
	t.Helper()

	var position storedPosition
	row := p.db.QueryRowContext(t.Context(), `
		SELECT id, name, price, is_active
		FROM platform.products
		WHERE restaurant_id = $1 AND external_id = $2`, restaurant, externalID)

	switch err := row.Scan(&position.ID, &position.Name, &position.Price, &position.IsActive); {
	case errors.Is(err, sql.ErrNoRows):
		return storedPosition{}, false
	case err != nil:
		t.Fatalf("прочитать позицию %q заведения %s: %v", externalID, restaurant, err)
	}

	return position, true
}

// lastSyncedAt читает время последней синхронизации каталога заведения; nil означает, что
// меню не забирали ни разу.
func (p *platform) lastSyncedAt(t *testing.T, restaurant uuid.UUID) *time.Time {
	t.Helper()

	var syncedAt *time.Time
	row := p.db.QueryRowContext(t.Context(),
		`SELECT last_synced_at FROM platform.restaurants WHERE id = $1`, restaurant)
	if err := row.Scan(&syncedAt); err != nil {
		t.Fatalf("прочитать время синхронизации заведения %s: %v", restaurant, err)
	}

	return syncedAt
}

// item находит на странице выдачи позицию по названию: тест называет то, что проверяет,
// а не место, на которое позиция попала в порядке выдачи.
func productOnPage(t *testing.T, page customer.ProductPage, name string) customer.Product {
	t.Helper()

	for _, card := range page.Items {
		if card.Name == name {
			return card
		}
	}

	t.Fatalf("на витрине нет позиции %q, есть %v", name, productNames(page))

	return customer.Product{}
}

// menuOf собирает меню из count различимых позиций: их больше, чем помещается на одну
// страницу выгрузки, поэтому площадке приходится идти за второй.
func menuOf(count int) []boxItem {
	items := make([]boxItem, 0, count)
	for number := 1; number <= count; number++ {
		items = append(items, boxItem{
			ExternalID: fmt.Sprintf("позиция-%d", number),
			Name:       fmt.Sprintf("Позиция №%d", number),
			Price:      int64(number),
		})
	}

	return items
}

func TestMenuOfRestaurantReachesTheShowcaseAfterSync(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(
		boxItem{ExternalID: "croissant", Name: "Круассан с миндалём", Description: "Миндальный крем", Price: 45000},
		boxItem{ExternalID: "baguette", Name: "Багет", Price: 12000, OutOfStock: true},
	)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)

	platform.refreshCatalog(t, registered.Key)

	page := platform.menu(t, registered.ID, nil)
	if len(page.Items) != 2 {
		t.Fatalf("на витрине %d позиций, ожидали обе из меню заведения: %v", len(page.Items), productNames(page))
	}

	croissant := productOnPage(t, page, "Круассан с миндалём")
	if croissant.Price != 45000 {
		t.Errorf("позиция приехала за %d, ожидали 45000 копеек", croissant.Price)
	}
	if croissant.Description == nil || *croissant.Description != "Миндальный крем" {
		t.Errorf("описание приехало как %v, ожидали «Миндальный крем»", croissant.Description)
	}
	// Стоп-лист заведения доезжает до витрины как есть: позиция видна, но заказать её
	// нельзя — исчезни она совсем, покупатель искал бы блюдо, которое вчера видел.
	if productOnPage(t, page, "Багет").IsAvailable {
		t.Error("позиция из стоп-листа заведения приехала на витрину доступной")
	}
}

func TestPositionIsRecognisedByExternalIdentifierAndKeepsItsCardBetweenSyncs(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	before, _ := platform.storedPosition(t, registered.ID, "croissant")

	// Заведение поправило и цену, и название: для площадки это та же позиция — внешний
	// идентификатор не менялся.
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан с миндалём", Price: 47000})
	platform.refreshCatalog(t, registered.Key)

	page := platform.menu(t, registered.ID, nil)
	if len(page.Items) != 1 {
		t.Fatalf("на витрине %d позиций, ожидали одну: та же позиция не заводится второй раз: %v",
			len(page.Items), productNames(page))
	}

	after, _ := platform.storedPosition(t, registered.ID, "croissant")
	if after.ID != before.ID {
		t.Errorf("позиция сменила идентификатор с %s на %s: ссылка на карточку, которая уже у покупателя, перестала работать",
			before.ID, after.ID)
	}
	if page.Items[0].Name != "Круассан с миндалём" || page.Items[0].Price != 47000 {
		t.Errorf("на витрине %q за %d, ожидали правку заведения «Круассан с миндалём» за 47000",
			page.Items[0].Name, page.Items[0].Price)
	}
}

func TestPositionThatVanishedFromTheDumpDisappearsFromTheShowcaseButKeepsItsCard(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(
		boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000},
		boxItem{ExternalID: "baguette", Name: "Багет", Price: 12000},
	)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	baguette, _ := platform.storedPosition(t, registered.ID, "baguette")

	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали только оставшийся в меню «Круассан»", names)
	}
	if document := platform.refusal(t,
		platformhttp.CustomerAPIPrefix+"/products/"+baguette.ID.String(), http.StatusNotFound); document.Code != "product_not_found" {
		t.Errorf("карточка исчезнувшей позиции отвечает %q, ожидали product_not_found", document.Code)
	}

	// Позиция деактивирована, а не удалена: вернувшееся назавтра блюдо получает ту же
	// карточку, а не новую ссылку взамен разосланной.
	stored, exists := platform.storedPosition(t, registered.ID, "baguette")
	if !exists {
		t.Fatal("исчезнувшая позиция удалена из каталога вместе с историей, ожидали деактивацию")
	}
	if stored.IsActive {
		t.Error("позиция, которой нет в выгрузке заведения, осталась активной")
	}
}

func TestPositionThatCameBackReturnsToTheShowcaseWithTheSameCard(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	before, _ := platform.storedPosition(t, registered.ID, "croissant")

	bakery.setMenu()
	platform.refreshCatalog(t, registered.Key)

	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	platform.refreshCatalog(t, registered.Key)

	after, exists := platform.storedPosition(t, registered.ID, "croissant")
	if !exists || !after.IsActive {
		t.Fatal("вернувшаяся в меню позиция не вернулась на витрину")
	}
	if after.ID != before.ID {
		t.Errorf("вернувшаяся позиция получила идентификатор %s вместо прежнего %s: рядом со старой карточкой появилась вторая",
			after.ID, before.ID)
	}
}

// Ради этого ручка и существует: заведение поправило цену и хочет, чтобы покупатель
// увидел новую, не дожидаясь очередного цикла синхронизации.
func TestPriceChangedInTheRestaurantReachesTheShowcaseWithoutWaitingForTheTimer(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 52000})
	visitsBefore := bakery.visited()

	platform.refreshCatalog(t, registered.Key)

	if bakery.visited() == visitsBefore {
		t.Fatal("площадка не сходила за меню на просьбу обновиться немедленно")
	}
	if got := platform.menu(t, registered.ID, nil).Items[0].Price; got != 52000 {
		t.Errorf("на витрине цена %d, ожидали новую 52000 сразу после просьбы обновиться", got)
	}
}

func TestRefreshTakesTheMenuOfTheRestaurantThatAskedAndNotOfItsNeighbour(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery, coffee := newBox(t), newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	coffee.setMenu(boxItem{ExternalID: "cappuccino", Name: "Капучино", Price: 25000})
	baton := platform.connect(t, "Пекарня «Батон»", bakery)
	steam := platform.connect(t, "Кофейня «Пар»", coffee)

	platform.refreshCatalog(t, baton.Key)

	if coffee.visited() != 0 {
		t.Error("площадка сходила за меню соседнего заведения по чужой просьбе")
	}
	if names := productNames(platform.menu(t, steam.ID, nil)); len(names) != 0 {
		t.Errorf("в меню соседнего заведения %v, ожидали пусто: его меню никто не просил забирать", names)
	}
}

func TestMenuLongerThanOnePageIsTakenWhole(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(menuOf(dumpLongerThanOnePage)...)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)

	platform.refreshCatalog(t, registered.Key)

	// Выгрузка страничная, и площадка обязана дойти до конца: остановись она на первой
	// странице — на витрине оказалось бы меню, которого у заведения нет.
	if total := platform.menu(t, registered.ID, nil).Page.Total; total != dumpLongerThanOnePage {
		t.Errorf("на витрине %d позиций, ожидали всё меню из %d", total, dumpLongerThanOnePage)
	}
	if bakery.visited() < 2 {
		t.Errorf("площадка сходила за меню %d раз: меню длиннее страницы забирается страницами",
			bakery.visited())
	}
}

func TestSyncBrokenInTheMiddleOfTheDumpLeavesThePreviousCatalogUntouched(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Заведение выложило новое меню, но отдало только первую страницу и сломалось.
	bakery.setMenu(menuOf(dumpLongerThanOnePage)...)
	var pagesGiven atomic.Int64
	bakery.answerWith(func(w http.ResponseWriter, r *http.Request) {
		if pagesGiven.Add(1) == 1 {
			bakery.writeMenuPage(w, r)

			return
		}

		w.WriteHeader(http.StatusInternalServerError)
	})

	platform.refreshCatalog(t, registered.Key)

	// Ни одной позиции недочитанной выгрузки на витрине нет: применённая наполовину она
	// оставила бы заведение с меню, которого нет ни у него, ни у площадки до синхронизации.
	page := platform.menu(t, registered.ID, nil)
	if names := productNames(page); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог из одного «Круассана»", names)
	}
}

func TestDumpLargerThanTheCeilingIsRefusedEntirely(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Заведение отдаёт страницы бесконечно: каждая с новыми позициями, а общее число в
	// ответе такое, что конца выгрузки не будет никогда.
	bakery.answerWith(func(w http.ResponseWriter, r *http.Request) {
		offset := intQuery(r, "offset", 0)
		limit := intQuery(r, "limit", 100)

		items := make([]menuItemDocument, 0, limit)
		for number := offset; number < offset+limit; number++ {
			items = append(items, menuItemDocument{
				ExternalID: fmt.Sprintf("позиция-%d", number),
				Name:       fmt.Sprintf("Позиция №%d", number),
				Price:      1,
			})
		}

		writeMenu(w, menuPageDocument{
			Items: items,
			Page:  pageMetaDocument{Limit: limit, Offset: offset, Total: endlessMenu},
		})
	})

	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог: выгрузка без конца не применяется вовсе", names)
	}
}

func TestResponseLargerThanTheAllowedSizeIsRefusedEntirely(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Заведение отвечает страницей, которой нет конца. Само меню в ней безупречно —
	// приняв ответ, площадка положила бы «Багет» на витрину, — и негоден ответ ровно
	// одним: его размером. Дочитывать его площадка не обязана: тело неограниченного
	// размера читается в память до того, как хоть одна проверка успеет о нём высказаться.
	bakery.answerWith(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[{"external_id":"baguette","name":"Багет","price":12000,`+
			`"is_available":true}],"page":{"limit":500,"offset":0,"total":1},"note":"`)

		filler := strings.Repeat("x", 1<<16)
		for written := 0; written < responseLargerThanAllowed; written += len(filler) {
			if _, err := fmt.Fprint(w, filler); err != nil {
				// Площадка перестала читать и закрыла соединение — ровно это и проверяется.
				return
			}
		}

		// Ответ дописывается до конца: негоден он размером, а не обрывом — иначе тест
		// проходил бы и без потолка, на одной только ошибке разбора.
		_, _ = fmt.Fprint(w, `"}`)
	})

	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог: ответ сверх потолка не применяется вовсе", names)
	}
}

func TestPageWithMoreItemsThanAskedForIsRefusedEntirely(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Заведение отдаёт страницу больше запрошенной: страничность — часть контракта, а не
	// рекомендация, и площадка не обязана принимать выдачу неизвестного размера.
	bakery.answerWith(func(w http.ResponseWriter, r *http.Request) {
		limit := intQuery(r, "limit", 100)
		items := make([]menuItemDocument, 0, limit+1)
		for number := range limit + 1 {
			items = append(items, menuItemDocument{
				ExternalID: fmt.Sprintf("позиция-%d", number),
				Name:       fmt.Sprintf("Позиция №%d", number),
				Price:      1,
			})
		}

		writeMenu(w, menuPageDocument{
			Items: items,
			Page:  pageMetaDocument{Limit: limit, Offset: 0, Total: int64(len(items))},
		})
	})

	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог", names)
	}
}

func TestSameExternalIdentifierTwiceInOneDumpIsRefusedEntirely(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Две позиции с одним внешним идентификатором делают выгрузку неразрешимой: площадка
	// выбирала бы между ними наугад, и на витрине оказалось бы то одно блюдо, то другое.
	bakery.setMenu(
		boxItem{ExternalID: "twin", Name: "Слойка с творогом", Price: 15000},
		boxItem{ExternalID: "twin", Name: "Слойка с вишней", Price: 16000},
	)

	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог", names)
	}
}

func TestPositionThatBreaksTheContractSpoilsTheWholeDumpAndNotOnlyItself(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Одна позиция без названия — это ошибка в выгрузке, а не повод показать покупателю
	// «почти всё меню»: пропустив её, площадка тихо потеряла бы блюдо.
	bakery.setMenu(
		boxItem{ExternalID: "baguette", Name: "Багет", Price: 12000},
		boxItem{ExternalID: "nameless", Name: "", Price: 1000},
	)

	platform.refreshCatalog(t, registered.Key)

	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог целиком", names)
	}
}

func TestRestaurantThatDoesNotAnswerDoesNotHoldTheSyncForever(t *testing.T) {
	t.Parallel()

	platform := newPlatformWaitingForBox(t, boxSilenceLimit)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	platform.refreshCatalog(t, registered.Key)

	// Заведение приняло соединение и замолчало: без таймаута синхронизация ждала бы его
	// столько, сколько оно решит молчать.
	bakery.answerWith(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	started := time.Now()
	platform.refreshCatalog(t, registered.Key)

	if waited := time.Since(started); waited > boxSilenceLimit*silencePatience {
		t.Errorf("площадка ждала молчащее заведение %s при пределе %s", waited, boxSilenceLimit)
	}
	if names := productNames(platform.menu(t, registered.ID, nil)); len(names) != 1 || names[0] != "Круассан" {
		t.Errorf("на витрине %v, ожидали прежний каталог", names)
	}
}

func TestTimeOfTheLastSyncIsRecorded(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	bakery.setMenu(boxItem{ExternalID: "croissant", Name: "Круассан", Price: 45000})
	registered := platform.connect(t, "Пекарня «Батон»", bakery)

	// До первой синхронизации отметки нет вовсе: «не синхронизировалось ни разу» и
	// «синхронизировалось в эпоху Unix» — разные ответы тому, кто разбирается, почему на
	// витрине заведения пусто.
	if syncedAt := platform.lastSyncedAt(t, registered.ID); syncedAt != nil {
		t.Errorf("время синхронизации %s, а меню ещё ни разу не забирали", syncedAt)
	}

	before := time.Now()
	platform.refreshCatalog(t, registered.Key)

	syncedAt := platform.lastSyncedAt(t, registered.ID)
	if syncedAt == nil {
		t.Fatal("после синхронизации время последней выгрузки не записано")
	}
	if syncedAt.Before(before.Add(-clockSkew)) {
		t.Errorf("время синхронизации %s раньше самой синхронизации %s", syncedAt, before)
	}
}

func TestFailedSyncDoesNotPassForASuccessfulOne(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	bakery := newBox(t)
	registered := platform.connect(t, "Пекарня «Батон»", bakery)
	bakery.answerWith(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	platform.refreshCatalog(t, registered.Key)

	// Отметка времени, пережившая неудачную выгрузку, утверждала бы, что меню обновлено,
	// тогда как на витрине лежит прежнее.
	if syncedAt := platform.lastSyncedAt(t, registered.ID); syncedAt != nil {
		t.Errorf("после неудачной выгрузки записано время синхронизации %s", syncedAt)
	}
}

const (
	// dumpLongerThanOnePage — меню, которое не помещается на одну страницу выгрузки:
	// площадка просит у заведения 500 позиций за раз.
	dumpLongerThanOnePage = 501
	// endlessMenu — общее число позиций, которое объявляет заведение, отдающее страницы
	// бесконечно: сколько бы площадка ни забрала, конца выгрузки не будет.
	endlessMenu = 1 << 40
	// responseLargerThanAllowed — сколько байт отдаёт заведение, нарушающее потолок
	// размера ответа (20 МБ). Чуть больше потолка: проверяется, что площадка перестаёт
	// читать, а не то, сколько именно она успела прочитать.
	responseLargerThanAllowed = 21 << 20

	// boxSilenceLimit — сколько площадка, поднятая для теста про молчащее заведение, ждёт
	// ответа, и silencePatience — во сколько раз дольше предела тест согласен ждать её
	// саму: на загруженной машине запрос завершается не в тот же миг, что и таймаут.
	boxSilenceLimit = 300 * time.Millisecond
	silencePatience = 10

	// clockSkew — на сколько часы базы, которая пишет `now()`, и часы теста могут
	// разойтись: сравниваются два разных источника времени, и требовать от них совпадения
	// до наносекунды значило бы проверять не правило, а синхронизацию часов.
	clockSkew = time.Minute
)
