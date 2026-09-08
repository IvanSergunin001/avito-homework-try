package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// firstAttempt — номер первой попытки передать заказ заведению.
//
// Пока площадка не повторяет передачу, номер всегда этот, и заведение по нему видит, что
// доставка первая. Повторы появятся вместе с восстановлением заказа, зависшего в
// `pending`; заголовок контракта заведён под них с самого начала — иначе первый же повтор
// приехал бы к заведению неотличимым от нового заказа.
const firstAttempt = 1

// catalogCatchUpTimeout — сколько отведено внеочередной выгрузке меню, которую площадка
// затевает сама, обнаружив, что цена на витрине разошлась с ценой заведения.
//
// Предел свой, а не общий с плановой синхронизацией: ту ведёт служба и ждать её некому, а
// эта запущена запросом покупателя, который уже закончился, — и висеть после него дольше,
// чем живёт сам повод, ей незачем.
const catalogCatchUpTimeout = 30 * time.Second

// OrderedItem — строка корзины покупателя: какой товар и сколько его взяли.
//
// Цены здесь нет: её знает площадка, и присланной клиентом сумме она не верит — та не
// приезжает в сценарий даже значением, поэтому «не принять» её невозможно забыть.
type OrderedItem struct {
	// ProductID — товар площадки, который покупатель положил в корзину.
	ProductID uuid.UUID
	// Quantity — сколько единиц товара он взял.
	Quantity int
}

// Placement — заказ, каким его прислал покупатель: кто, где, что и куда.
type Placement struct {
	// OrderID — идентификатор заказа, выданный клиентом (uuid v7).
	OrderID uuid.UUID
	// CustomerID — покупатель из заголовка `User-Id`.
	CustomerID uuid.UUID
	// RestaurantID — заведение, в котором оформляется заказ.
	RestaurantID uuid.UUID
	// Address — адрес доставки в свободной форме.
	Address string
	// Items — состав корзины.
	Items []OrderedItem
}

// CatalogPosition — позиция каталога, из которой собирается строка заказа: то, что
// площадка знает о товаре в момент оформления.
//
// Это не Product: витрине внешний идентификатор не показывают вовсе, а заказу он
// необходим — им заведение узнаёт позицию в переданном ему заказе.
type CatalogPosition struct {
	// ProductID — идентификатор товара на площадке.
	ProductID uuid.UUID
	// ExternalID — чем эта позиция зовётся в системе заведения.
	ExternalID string
	// Name — название товара, каким его видит покупатель.
	Name string
	// Price — цена товара в копейках.
	Price domain.Money
}

// OrderedProducts — товары площадки глазами оформления заказа.
//
// Порт отдельный от Products, хотя данные за ними в одной таблице: витрине нужны цена и
// название, оформлению — ещё и то, чем позиция зовётся у заведения. Один порт на оба
// сценария означал бы, что чтение витрины умеет отдавать реквизиты интеграции.
type OrderedProducts interface {
	// Positions отдаёт позиции каталога работающего заведения по идентификаторам товаров.
	// Товара, которого в меню этого заведения нет, в выдаче просто нет — решение о том,
	// что с этим делать, принимает сценарий.
	Positions(ctx context.Context, restaurant uuid.UUID, ids []uuid.UUID) ([]CatalogPosition, error)
}

// Orders — хранилище заказов площадки.
type Orders interface {
	// Add записывает оформленный заказ. Второй результат отвечает, завела ли площадка
	// заказ сейчас: ложь означает, что идентификатор уже занят, и отдан заказ, который
	// его занимает, — чей он и тем ли запросом оформлен, решает предметная область.
	//
	// Отдельного «есть ли уже такой» у хранилища нет намеренно: между вопросом и записью
	// помещается второе нажатие «Заказать», и обе попытки получили бы ответ «нет».
	Add(ctx context.Context, order domain.Order) (domain.Order, bool, error)
	// SaveStatus записывает новое состояние заказа: сам статус, инициатора и причину
	// отмены. Состав и сумма не переписываются никогда — заказ не меняется задним числом.
	SaveStatus(ctx context.Context, order domain.Order) error
	// FindForCustomer отдаёт заказ, принадлежащий этому покупателю. Если заказа нет либо
	// он чужой — domain.ErrOrderNotFound.
	FindForCustomer(ctx context.Context, id, customer uuid.UUID) (domain.Order, error)
}

// OrderIntake — приём заказа заведением по ту сторону партнёрского контракта.
//
// Вызов синхронный, и это часть сценария, а не деталь реализации: заведение — источник
// истины по наличию и цене, и покупатель узнаёт судьбу заказа сразу, а не письмом через
// минуту.
type OrderIntake interface {
	// Accept передаёт заказ заведению, живущему по адресу baseURL, и дожидается решения.
	// attempt — номер попытки доставки: по нему заведение отличает повтор от нового заказа.
	//
	// Реализация обязана ограничивать себя сама: без таймаута заведение, принявшее
	// соединение и замолчавшее, держало бы покупателя перед экраном бесконечно.
	Accept(ctx context.Context, baseURL string, order domain.Order, attempt int) error
}

// RestaurantUnavailableError — заведение не ответило на переданный заказ: не отозвалось
// вовсе, ответило не по контракту либо не уложилось в отведённое ему время.
//
// Различие между «не отозвалось» и «не успело» вынесено в отдельное поле, потому что оно
// доезжает до покупателя разными кодами ответа: молчащее заведение — это сорванный вызов
// (502), не успевшее — истёкшее ожидание (504), и клиент вправе повторить их по-разному.
type RestaurantUnavailableError struct {
	// TimedOut — заведение не уложилось в отведённое ему время.
	TimedOut bool
	// Err — что именно случилось; наружу не показывается, но попадает в лог площадки.
	Err error
}

// Error возвращает причину, по которой заведение не ответило.
func (e RestaurantUnavailableError) Error() string {
	return "заведение недоступно: " + e.Err.Error()
}

// Unwrap отдаёт исходную ошибку: она нужна тому, кто разбирается, почему заведение молчит.
func (e RestaurantUnavailableError) Unwrap() error {
	return e.Err
}

// RestaurantRefusalError — заведение отказалось взять заказ и назвало причину.
//
// Это не сбой связи и не поломка: заведение ответило по контракту, и ответ его —
// «выполнить не можем, потому что». Позиция названа тем идентификатором, которым её знает
// само заведение: перевести его в товар площадки может только тот, у кого на руках
// состав заказа.
type RestaurantRefusalError struct {
	// Reason — машиночитаемая причина отказа.
	Reason domain.RefusalReason
	// ProductExternalID — позиция, из-за которой отказан заказ, в идентификаторах
	// заведения; у отказа закрытого заведения её нет.
	ProductExternalID string
	// CurrentPrice — актуальная цена позиции у заведения; приходит с причиной
	// domain.PriceChanged.
	CurrentPrice domain.Money
	// Detail — пояснение для человека.
	Detail string
}

// Error возвращает причину, по которой заведение не взяло заказ.
func (e RestaurantRefusalError) Error() string {
	return fmt.Sprintf("заведение не приняло заказ (%s): %s", e.Reason, e.Detail)
}

// CatalogRefresh — внеочередное обновление каталога заведения.
//
// Оформление зависит от синхронизации ровно в одной точке: заведение ответило, что цена
// не та, — значит, витрина показывает вчерашнюю правду, и правит её не человек по жалобе,
// а сама площадка. Порт здесь узкий специально: оформлению нужно одно действие, а не вся
// синхронизация каталога.
type CatalogRefresh interface {
	// Refresh забирает меню заведения и применяет его к каталогу площадки.
	Refresh(ctx context.Context, partner domain.Partner) error
}

// Ordering — оформление заказа и его просмотр покупателем.
//
// Сценарий один на оба действия намеренно: заказ, который покупатель оформил, и заказ,
// который он потом открывает, — один и тот же заказ, и расхождение между ними было бы
// дефектом, а не свойством.
type Ordering struct {
	partners Partners
	products OrderedProducts
	orders   Orders
	intake   OrderIntake
	catalog  CatalogRefresh
	log      *slog.Logger
}

// NewOrdering собирает оформление над заведениями, каталогом, хранилищем заказов, приёмом
// заказа заведением и внеочередным обновлением каталога.
func NewOrdering(
	partners Partners, products OrderedProducts, orders Orders, intake OrderIntake,
	catalog CatalogRefresh, log *slog.Logger,
) *Ordering {
	return &Ordering{
		partners: partners, products: products, orders: orders,
		intake: intake, catalog: catalog, log: log,
	}
}

// Place оформляет заказ: считает сумму, записывает заказ и передаёт его заведению.
//
// Порядок шагов — само правило, а не удобство реализации. Заказ ложится в хранилище в
// состоянии `pending` до обращения к заведению: иначе площадка, упавшая между вызовом и
// записью, потеряла бы заказ, который заведение уже готовит. Обратный порядок дешевле
// только до первого такого случая — а разбирать его пришлось бы по логам заведения.
//
// Успешный ответ заведения переводит заказ в `under_review` — и переводит его агрегат, а
// не этот сценарий: правило «кто и когда двигает заказ» живёт в матрице переходов.
// Заведение, не ответившее вовсе, оставляет заказ в `pending`: он записан, покупатель
// видит отказ, а починить такой заказ можно повторной отправкой по тому же
// идентификатору.
//
// Повтор проходит тот же путь целиком: заказ собирается по каталогу заново и только потом
// упирается в занятый идентификатор. Обратный порядок — сначала спросить хранилище, есть
// ли уже такой заказ, — означал бы чтение перед вставкой, между которыми помещается второе
// нажатие «Заказать»: оба чтения ответили бы «нет». Цена выбранного порядка в том, что
// повтор заказа, позиция которого успела исчезнуть с витрины, получит «товар не найден»
// вместо своего заказа.
func (o *Ordering) Place(ctx context.Context, placement Placement) (domain.Order, bool, error) {
	// Заведение берётся со стороны интеграции, а не с витрины: оформлению нужен адрес
	// коробки, и он же отвечает на вопрос, работает ли заведение вообще.
	partner, err := o.partners.Find(ctx, placement.RestaurantID)
	if err != nil {
		return domain.Order{}, false, err
	}

	items, err := o.snapshot(ctx, placement)
	if err != nil {
		return domain.Order{}, false, err
	}

	order, err := domain.PlaceOrder(placement.OrderID, placement.CustomerID, placement.RestaurantID,
		placement.Address, items, now())
	if err != nil {
		return domain.Order{}, false, err
	}

	existing, placed, err := o.orders.Add(ctx, order)
	if err != nil {
		return domain.Order{}, false, err
	}
	if !placed {
		// Идентификатор занят: либо это повтор — и тогда покупатель получает свой заказ
		// таким, каким он есть сейчас, — либо тем же идентификатором оформляют другой
		// заказ, и это отказ. Заведение в обоих случаях не тревожат: заказ ему уже
		// передавали, и второй раз он был бы вторым заказом.
		repeated, err := existing.Repeat(order)

		return repeated, false, err
	}

	if err := o.intake.Accept(ctx, partner.BaseURL, order, firstAttempt); err != nil {
		return domain.Order{}, false, o.refusalOf(ctx, partner, order, err)
	}

	if err := order.TransitionTo(domain.UnderReview, domain.ByRestaurant, now()); err != nil {
		return domain.Order{}, false, fmt.Errorf("принять ответ заведения по заказу %s: %w", order.ID, err)
	}
	if err := o.orders.SaveStatus(ctx, order); err != nil {
		return domain.Order{}, false, err
	}

	return order, true, nil
}

// refusalOf переводит ответ заведения на переданный заказ в отказ, который увидит
// покупатель.
//
// Заведение отвечает в своих идентификаторах, покупатель читает ответ в идентификаторах
// площадки: позицию, из-за которой отказан заказ, называет состав самого заказа — он
// уехал заведению снимком, и позиция ищется в нём, а не в каталоге, который к этому
// моменту мог измениться.
//
// Всё, что отказом заведения не является — молчание, ответ не по контракту, поломка
// внутри площадки, — проходит насквозь: причины, по которой покупатель может что-то
// решить, у такого ответа нет.
func (o *Ordering) refusalOf(
	ctx context.Context, partner domain.Partner, order domain.Order, err error,
) error {
	var refusal RestaurantRefusalError
	if !errors.As(err, &refusal) {
		return err
	}

	if refusal.Reason == domain.PriceChanged {
		o.catchUpWithTheMenu(ctx, partner)
	}

	return domain.OrderRefusedError{
		Reason:       refusal.Reason,
		ProductID:    order.ProductOf(refusal.ProductExternalID),
		CurrentPrice: refusal.CurrentPrice,
		Detail:       refusal.Detail,
	}
}

// catchUpWithTheMenu отправляет площадку за меню заведения вне очереди: цена, по которой
// заказ отказан, на витрине всё ещё старая, и следующий покупатель наткнулся бы на тот же
// отказ.
//
// Синхронизация идёт мимо покупателя: он уже получил отказ с новой ценой, а меню целиком
// заведение отдаёт дольше, чем площадка готова держать человека перед экраном, — предел
// ожидания выгрузки для того и больше предела ожидания заказа. Поэтому и контекст свой:
// запрос покупателя вот-вот закончится и отменил бы выгрузку на середине.
//
// Неудача остаётся в логе: позвать её было решением площадки, а не покупателя, и
// рассказывать ему о том, чего он не просил, незачем — на витрине цена догонит
// реальность следующей плановой выгрузкой.
func (o *Ordering) catchUpWithTheMenu(ctx context.Context, partner domain.Partner) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogCatchUpTimeout)
		defer cancel()

		if err := o.catalog.Refresh(ctx, partner); err != nil {
			o.log.ErrorContext(ctx, "extraordinary catalog sync failed",
				slog.String("restaurant_id", partner.ID.String()), slog.Any("error", err))
		}
	}()
}

// Order отдаёт заказ покупателя: его состав, сумму и состояние.
func (o *Ordering) Order(ctx context.Context, id, customer uuid.UUID) (domain.Order, error) {
	return o.orders.FindForCustomer(ctx, id, customer)
}

// snapshot переводит корзину в состав заказа, беря название и цену из каталога площадки.
//
// Снимок делается здесь и один раз: заказ уезжает заведению и живёт дальше теми
// значениями, которые покупатель видел на витрине, а не теми, которые окажутся в каталоге
// к моменту следующего чтения.
func (o *Ordering) snapshot(ctx context.Context, placement Placement) ([]domain.OrderItem, error) {
	ids := make([]uuid.UUID, 0, len(placement.Items))
	for _, ordered := range placement.Items {
		ids = append(ids, ordered.ProductID)
	}

	positions, err := o.products.Positions(ctx, placement.RestaurantID, ids)
	if err != nil {
		return nil, err
	}

	catalog := make(map[uuid.UUID]CatalogPosition, len(positions))
	for _, position := range positions {
		catalog[position.ProductID] = position
	}

	items := make([]domain.OrderItem, 0, len(placement.Items))
	for _, ordered := range placement.Items {
		position, known := catalog[ordered.ProductID]
		if !known {
			// Товара нет в меню этого заведения — заказать его нельзя, и заказ не
			// оформляется целиком: собрать «почти тот» заказ площадка не вправе.
			return nil, domain.ErrProductNotFound
		}

		items = append(items, domain.OrderItem{
			ProductID:  position.ProductID,
			ExternalID: position.ExternalID,
			Name:       position.Name,
			Price:      position.Price,
			Quantity:   ordered.Quantity,
		})
	}

	return items, nil
}

// now — момент, которым отмечается заказ.
//
// Округление до микросекунды не косметика: столько хранит PostgreSQL, и без него заказ,
// только что отданный клиенту, отличался бы временем от него же, прочитанного следующим
// запросом.
func now() time.Time {
	return time.Now().Truncate(time.Microsecond)
}
