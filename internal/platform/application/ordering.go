package application

import (
	"context"
	"fmt"
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
	// Add записывает оформленный заказ.
	Add(ctx context.Context, order domain.Order) error
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
}

// NewOrdering собирает оформление над заведениями, каталогом, хранилищем заказов и
// приёмом заказа заведением.
func NewOrdering(partners Partners, products OrderedProducts, orders Orders, intake OrderIntake) *Ordering {
	return &Ordering{partners: partners, products: products, orders: orders, intake: intake}
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
func (o *Ordering) Place(ctx context.Context, placement Placement) (domain.Order, error) {
	// Заведение берётся со стороны интеграции, а не с витрины: оформлению нужен адрес
	// коробки, и он же отвечает на вопрос, работает ли заведение вообще.
	partner, err := o.partners.Find(ctx, placement.RestaurantID)
	if err != nil {
		return domain.Order{}, err
	}

	// Отключённое заведение перестаёт быть партнёром площадки целиком — заказы ему не
	// передаются. Отбор стоит здесь, а не в запросе хранилища: тем же `Find` пользуется
	// админская поверхность, которой отключённые заведения как раз нужны.
	if !partner.IsActive {
		return domain.Order{}, domain.ErrRestaurantNotFound
	}

	items, err := o.snapshot(ctx, placement)
	if err != nil {
		return domain.Order{}, err
	}

	order, err := domain.PlaceOrder(placement.OrderID, placement.CustomerID, placement.RestaurantID,
		placement.Address, items, now())
	if err != nil {
		return domain.Order{}, err
	}

	if err := o.orders.Add(ctx, order); err != nil {
		return domain.Order{}, err
	}

	if err := o.intake.Accept(ctx, partner.BaseURL, order, firstAttempt); err != nil {
		return domain.Order{}, err
	}

	if err := order.TransitionTo(domain.UnderReview, domain.ByRestaurant, now()); err != nil {
		return domain.Order{}, fmt.Errorf("принять ответ заведения по заказу %s: %w", order.ID, err)
	}
	if err := o.orders.SaveStatus(ctx, order); err != nil {
		return domain.Order{}, err
	}

	return order, nil
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
