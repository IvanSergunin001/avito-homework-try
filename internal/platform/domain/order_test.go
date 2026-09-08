// Тесты предметной области идут без всякой инфраструктуры: ни базы, ни HTTP, ни
// подменённых зависимостей — правила оформления заказа и его переходов не зависят ни от
// чего, кроме самих себя, и проверяются вызовом функции.
//
// Имя теста называет правило, а не функцию под тестом: по списку упавших тестов должно
// быть видно, какое правило площадки нарушено.
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// placedAt — момент оформления заказа. Время в этих тестах — просто отметка: правила
// перехода от него не зависят, а вот отметка о том, когда заказ последний раз менялся,
// зависит.
var placedAt = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

// statuses — все состояния заказа. Тесты матрицы перебирают их целиком: правило «отсюда
// туда нельзя» проверяется по всей таблице, а не по паре выбранных клеток.
var statuses = []domain.OrderStatus{
	domain.Pending, domain.UnderReview, domain.Confirmed,
	domain.Cooking, domain.Delivering, domain.Completed, domain.Cancelled,
}

// actors — все инициаторы переходов.
var actors = []domain.Actor{domain.ByCustomer, domain.ByRestaurant, domain.BySupport}

// croissant и latte — товары каталога, из которых тесты собирают заказ.
func croissant(quantity int, price domain.Money) domain.OrderItem {
	return domain.OrderItem{
		ProductID:  uuid.New(),
		ExternalID: "croissant-almond",
		Name:       "Круассан с миндалём",
		Price:      price,
		Quantity:   quantity,
	}
}

func latte(quantity int, price domain.Money) domain.OrderItem {
	return domain.OrderItem{
		ProductID:  uuid.New(),
		ExternalID: "latte-300",
		Name:       "Латте 300 мл",
		Price:      price,
		Quantity:   quantity,
	}
}

// place оформляет заказ из перечисленных позиций.
func place(t *testing.T, items ...domain.OrderItem) domain.Order {
	t.Helper()

	order, err := domain.PlaceOrder(uuid.New(), uuid.New(), uuid.New(),
		"Москва, Лесная 7, кв. 12", items, placedAt)
	if err != nil {
		t.Fatalf("заказ не оформлен: %v", err)
	}

	return order
}

// orderIn доводит заказ до нужного состояния теми же переходами, какими он проходит их в
// жизни: подставить состояние снаружи нельзя — его меняет только агрегат, и это же
// свойство делает помощник честным.
func orderIn(t *testing.T, status domain.OrderStatus) domain.Order {
	t.Helper()

	order := place(t, croissant(1, 45000))
	if status == domain.Pending {
		return order
	}

	if status == domain.Cancelled {
		if err := order.Cancel(domain.ByCustomer, "", placedAt); err != nil {
			t.Fatalf("довести заказ до %q: %v", status, err)
		}

		return order
	}

	// Остальные состояния лежат на одном пути: заказ идёт по нему, пока не дойдёт до
	// нужного, и каждый шаг делает заведение — так же, как в жизни.
	for _, next := range []domain.OrderStatus{
		domain.UnderReview, domain.Confirmed, domain.Cooking, domain.Delivering, domain.Completed,
	} {
		if err := order.TransitionTo(next, domain.ByRestaurant, placedAt); err != nil {
			t.Fatalf("довести заказ до %q: %v", status, err)
		}
		if next == status {
			return order
		}
	}

	t.Fatalf("состояние %q не лежит на пути заказа", status)

	return domain.Order{}
}

// refusedTransition разбирает ошибку как отказ матрицы переходов.
func refusedTransition(t *testing.T, err error) domain.OrderTransitionError {
	t.Helper()

	var refusal domain.OrderTransitionError
	if !errors.As(err, &refusal) {
		t.Fatalf("ожидали отказ матрицы переходов, получили %v", err)
	}

	return refusal
}

// invalidOrder разбирает ошибку как отказ принять описание заказа.
func invalidOrder(t *testing.T, err error) domain.InvalidOrderError {
	t.Helper()

	var invalid domain.InvalidOrderError
	if !errors.As(err, &invalid) {
		t.Fatalf("ожидали отказ по описанию заказа, получили %v", err)
	}

	return invalid
}

func TestPlacedOrderWaitsInPendingUntilTheRestaurantHasBeenAsked(t *testing.T) {
	t.Parallel()

	if order := place(t, croissant(1, 45000)); order.Status != domain.Pending {
		t.Errorf("статус оформленного заказа %q, ожидали %q: заведение о заказе ещё не знает",
			order.Status, domain.Pending)
	}
}

func TestOrderTotalIsTheSumOfItsLinesCountedByThePlatform(t *testing.T) {
	t.Parallel()

	order := place(t, croissant(3, 45000), latte(2, 29900))

	if want := domain.Money(3*45000 + 2*29900); order.Total != want {
		t.Errorf("сумма заказа %d, ожидали %d: сумма считается по составу и ценам каталога",
			order.Total, want)
	}
}

func TestOrderWithoutItemsIsNotPlaced(t *testing.T) {
	t.Parallel()

	_, err := domain.PlaceOrder(uuid.New(), uuid.New(), uuid.New(),
		"Москва, Лесная 7, кв. 12", nil, placedAt)

	if invalidOrder(t, err).Reason == "" {
		t.Error("заказ без позиций отклонён без объяснения")
	}
}

func TestOrderKeepsNamesAndPricesOfTheMomentItWasPlaced(t *testing.T) {
	t.Parallel()

	items := []domain.OrderItem{croissant(2, 45000)}
	order := place(t, items...)

	// Каталог живёт своей жизнью: цена меняется, позиция переименовывается. Заказ,
	// собранный из прежних значений, об этом знать не должен.
	items[0].Price = 90000
	items[0].Name = "Круассан с миндалём (новый рецепт)"

	if order.Items[0].Price != 45000 || order.Items[0].Name != "Круассан с миндалём" {
		t.Errorf("позиция заказа %q по %d: заказ повторил за каталогом, а должен был остаться снимком",
			order.Items[0].Name, order.Items[0].Price)
	}
	if order.Total != 90000 {
		t.Errorf("сумма заказа %d, ожидали 90000: сумма посчитана по снимку, а не по каталогу", order.Total)
	}
}

func TestOrderRemembersWhatEachPositionIsCalledAtTheRestaurant(t *testing.T) {
	t.Parallel()

	order := place(t, croissant(1, 45000))

	if order.Items[0].ExternalID != "croissant-almond" {
		t.Errorf("внешний идентификатор позиции %q: без него заказ не передать заведению",
			order.Items[0].ExternalID)
	}
}

func TestRestaurantThatAcceptedTheOrderPutsItUnderReview(t *testing.T) {
	t.Parallel()

	order := place(t, croissant(1, 45000))
	acceptedAt := placedAt.Add(time.Second)

	if err := order.TransitionTo(domain.UnderReview, domain.ByRestaurant, acceptedAt); err != nil {
		t.Fatalf("заведение приняло заказ, а переход не выполнен: %v", err)
	}

	if order.Status != domain.UnderReview {
		t.Errorf("статус %q, ожидали %q", order.Status, domain.UnderReview)
	}
	if !order.UpdatedAt.Equal(acceptedAt) {
		t.Errorf("время изменения %s, ожидали %s: переход отмечает, когда заказ изменился",
			order.UpdatedAt, acceptedAt)
	}
}

func TestOrderMovesAlongTheStatusesTheMatrixAllowsForEachActor(t *testing.T) {
	t.Parallel()

	allowed := []struct {
		from domain.OrderStatus
		to   domain.OrderStatus
		by   domain.Actor
	}{
		{domain.Pending, domain.UnderReview, domain.ByRestaurant},
		{domain.Pending, domain.Cancelled, domain.ByCustomer},
		{domain.Pending, domain.Cancelled, domain.BySupport},
		{domain.UnderReview, domain.Confirmed, domain.ByRestaurant},
		{domain.UnderReview, domain.Cancelled, domain.ByCustomer},
		{domain.UnderReview, domain.Cancelled, domain.ByRestaurant},
		{domain.Confirmed, domain.Cooking, domain.ByRestaurant},
		{domain.Confirmed, domain.Cancelled, domain.ByCustomer},
		{domain.Cooking, domain.Delivering, domain.ByRestaurant},
		{domain.Cooking, domain.Cancelled, domain.BySupport},
		{domain.Delivering, domain.Completed, domain.ByRestaurant},
		{domain.Delivering, domain.Cancelled, domain.BySupport},
	}

	for _, transition := range allowed {
		order := orderIn(t, transition.from)

		if err := order.TransitionTo(transition.to, transition.by, placedAt); err != nil {
			t.Errorf("переход %s → %s по требованию %s не выполнен: %v",
				transition.from, transition.to, transition.by, err)

			continue
		}
		if order.Status != transition.to {
			t.Errorf("после перехода %s → %s статус %q", transition.from, transition.to, order.Status)
		}
	}
}

func TestTransitionForbiddenToThisActorIsRefusedEvenWhenAnotherActorMayMakeIt(t *testing.T) {
	t.Parallel()

	forbidden := []struct {
		from domain.OrderStatus
		to   domain.OrderStatus
		by   domain.Actor
		rule string
	}{
		{domain.Pending, domain.UnderReview, domain.ByCustomer,
			"на рассмотрение заказ переводит ответ заведения, а не покупатель"},
		{domain.UnderReview, domain.Confirmed, domain.ByCustomer,
			"за заказ берётся заведение, а не покупатель"},
		{domain.Cooking, domain.Cancelled, domain.ByRestaurant,
			"начав готовить, заведение отменяет заказ только через саппорт"},
		{domain.Delivering, domain.Cancelled, domain.ByCustomer,
			"продукты потрачены: покупатель отменяет заказ до начала готовки"},
		{domain.Pending, domain.Confirmed, domain.ByRestaurant,
			"заведение подтверждает заказ, который ему передали, а не мимо рассмотрения"},
		{domain.Confirmed, domain.Completed, domain.ByRestaurant,
			"заказ завершается доставкой, а не прыжком из подтверждения"},
		{domain.Delivering, domain.Cooking, domain.ByRestaurant,
			"заказ не возвращается к прошлому состоянию"},
	}

	for _, transition := range forbidden {
		order := orderIn(t, transition.from)

		err := order.TransitionTo(transition.to, transition.by, placedAt)
		if err == nil {
			t.Errorf("переход %s → %s по требованию %s выполнен, а не должен: %s",
				transition.from, transition.to, transition.by, transition.rule)

			continue
		}

		refusal := refusedTransition(t, err)
		if refusal.From != transition.from || refusal.To != transition.to || refusal.By != transition.by {
			t.Errorf("отказ описывает переход %s → %s по требованию %s, ожидали %s → %s по требованию %s",
				refusal.From, refusal.To, refusal.By, transition.from, transition.to, transition.by)
		}
		if order.Status != transition.from {
			t.Errorf("отказанный переход всё же изменил статус на %q", order.Status)
		}
	}
}

func TestRepeatedTransitionIntoTheStatusTheOrderIsAlreadyInIsRefused(t *testing.T) {
	t.Parallel()

	for _, status := range statuses {
		for _, actor := range actors {
			order := orderIn(t, status)

			if err := order.TransitionTo(status, actor, placedAt); err == nil {
				t.Errorf("повторный переход в %q по требованию %s выполнен, а не должен: "+
					"состояние уже это, и менять нечего", status, actor)
			}
		}
	}
}

func TestTerminalOrderAcceptsNoTransitionAtAllNoMatterWhoAsks(t *testing.T) {
	t.Parallel()

	for _, terminal := range []domain.OrderStatus{domain.Completed, domain.Cancelled} {
		if !terminal.IsTerminal() {
			t.Errorf("статус %q не терминальный, а должен быть", terminal)
		}

		for _, status := range statuses {
			for _, actor := range actors {
				order := orderIn(t, terminal)

				if err := order.TransitionTo(status, actor, placedAt); err == nil {
					t.Errorf("из терминального %q заказ ушёл в %q по требованию %s",
						terminal, status, actor)
				}
			}
		}
	}
}

func TestNonTerminalStatusesAreTheOnesTheOrderStillMovesFrom(t *testing.T) {
	t.Parallel()

	for _, status := range []domain.OrderStatus{
		domain.Pending, domain.UnderReview, domain.Confirmed, domain.Cooking, domain.Delivering,
	} {
		if status.IsTerminal() {
			t.Errorf("статус %q объявлен терминальным, а заказ из него ещё двигается", status)
		}
	}
}

func TestCustomerCancelsTheOrderUntilTheRestaurantStartsCooking(t *testing.T) {
	t.Parallel()

	for _, status := range []domain.OrderStatus{domain.Pending, domain.UnderReview, domain.Confirmed} {
		order := orderIn(t, status)

		if err := order.Cancel(domain.ByCustomer, "", placedAt); err != nil {
			t.Errorf("покупатель не отменил заказ в состоянии %q: %v — продукты ещё не потрачены",
				status, err)

			continue
		}
		if order.Status != domain.Cancelled {
			t.Errorf("статус отменённого заказа %q, ожидали %q", order.Status, domain.Cancelled)
		}
	}
}

func TestOrderBeingCookedIsCancelledOnlyThroughSupport(t *testing.T) {
	t.Parallel()

	cooking := orderIn(t, domain.Cooking)
	if err := cooking.Cancel(domain.ByCustomer, "", placedAt); err == nil {
		t.Error("покупатель отменил готовящийся заказ: продукты уже потрачены")
	}

	viaSupport := orderIn(t, domain.Cooking)
	if err := viaSupport.Cancel(domain.BySupport, "Закончились продукты", placedAt); err != nil {
		t.Fatalf("саппорт не отменил готовящийся заказ: %v", err)
	}
	if viaSupport.Status != domain.Cancelled {
		t.Errorf("статус %q, ожидали %q", viaSupport.Status, domain.Cancelled)
	}
}

func TestCancelledOrderRemembersWhoCancelledItAndWhy(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Confirmed)

	if err := order.Cancel(domain.BySupport, "Закончились продукты", placedAt); err != nil {
		t.Fatalf("отмена не выполнена: %v", err)
	}

	if order.CancelledBy != domain.BySupport {
		t.Errorf("инициатор отмены %q, ожидали %q", order.CancelledBy, domain.BySupport)
	}
	if order.CancellationReason != "Закончились продукты" {
		t.Errorf("причина отмены %q: её показывают покупателю", order.CancellationReason)
	}
}

func TestCustomerCancellingAnOrderThatIsAlreadyCancelledChangesNothing(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.UnderReview)
	if err := order.Cancel(domain.ByRestaurant, "Закончились продукты", placedAt); err != nil {
		t.Fatalf("заведение не отменило заказ: %v", err)
	}

	// Покупатель нажимает «Отменить» на заказе, который отменило заведение: отказом это
	// не является — заказ отменён, чего покупатель и хотел, — и переписывать чужую отмену
	// его нажатие не должно.
	cancelled, err := order.CancelByCustomer(placedAt)
	if err != nil {
		t.Fatalf("повторная отмена отказана: %v", err)
	}
	if cancelled {
		t.Error("повторная отмена объявлена изменением заказа: менять в отменённом заказе нечего")
	}
	if order.CancelledBy != domain.ByRestaurant || order.CancellationReason != "Закончились продукты" {
		t.Errorf("отмена записана на %q с причиной %q: нажатие покупателя переписало чужую отмену",
			order.CancelledBy, order.CancellationReason)
	}
}

func TestCustomerDoesNotCancelAnOrderThatWasAlreadyDelivered(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Completed)

	if _, err := order.CancelByCustomer(placedAt); err == nil {
		t.Error("покупатель отменил доставленный заказ: отменять в нём нечего")
	}
	if order.Status != domain.Completed {
		t.Errorf("статус заказа %q, ожидали %q", order.Status, domain.Completed)
	}
}

func TestCustomerCancellationIsRecordedAsMadeByTheCustomerAndWithoutAReason(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Confirmed)

	cancelled, err := order.CancelByCustomer(placedAt)
	if err != nil {
		t.Fatalf("покупатель не отменил подтверждённый заказ: %v", err)
	}
	if !cancelled {
		t.Fatal("отмена не объявлена изменением заказа, хотя заказ был живым")
	}
	if order.CancelledBy != domain.ByCustomer {
		t.Errorf("инициатор отмены %q, ожидали %q", order.CancelledBy, domain.ByCustomer)
	}
	if order.CancellationReason != "" {
		t.Errorf("причина отмены %q: покупатель объяснять свою отмену не обязан",
			order.CancellationReason)
	}
}

func TestCancellationByAnyoneButTheCustomerRequiresAReasonTheCustomerWillSee(t *testing.T) {
	t.Parallel()

	for _, actor := range []domain.Actor{domain.ByRestaurant, domain.BySupport} {
		order := orderIn(t, domain.UnderReview)

		if err := order.Cancel(actor, "   ", placedAt); err == nil {
			t.Errorf("%s отменил заказ без причины: покупателю нечего показать", actor)
		}
		if order.Status != domain.UnderReview {
			t.Errorf("отказанная отмена всё же изменила статус на %q", order.Status)
		}
	}
}

func TestOrderWithoutAddressIsNotPlaced(t *testing.T) {
	t.Parallel()

	_, err := domain.PlaceOrder(uuid.New(), uuid.New(), uuid.New(),
		"   ", []domain.OrderItem{croissant(1, 45000)}, placedAt)

	if invalidOrder(t, err).Reason == "" {
		t.Error("заказ без адреса отклонён без объяснения")
	}
}

func TestOrderWithoutACustomerIsNotPlaced(t *testing.T) {
	t.Parallel()

	_, err := domain.PlaceOrder(uuid.New(), uuid.Nil, uuid.New(),
		"Москва, Лесная 7, кв. 12", []domain.OrderItem{croissant(1, 45000)}, placedAt)

	if invalidOrder(t, err).Reason == "" {
		t.Error("заказ без покупателя отклонён без объяснения")
	}
}

func TestSameProductListedTwiceIsNotAnOrderThePlatformPlaces(t *testing.T) {
	t.Parallel()

	twice := croissant(1, 45000)

	_, err := domain.PlaceOrder(uuid.New(), uuid.New(), uuid.New(),
		"Москва, Лесная 7, кв. 12", []domain.OrderItem{twice, twice}, placedAt)

	if invalidOrder(t, err).Reason == "" {
		t.Error("повторённая позиция принята: заведение проверяло бы наличие по каждой строке отдельно")
	}
}

func TestOrderOfZeroUnitsIsNotPlaced(t *testing.T) {
	t.Parallel()

	for _, quantity := range []int{0, -1, 101} {
		_, err := domain.PlaceOrder(uuid.New(), uuid.New(), uuid.New(),
			"Москва, Лесная 7, кв. 12", []domain.OrderItem{croissant(quantity, 45000)}, placedAt)

		if err == nil {
			t.Errorf("заказ на %d единиц принят, а количество объявлено от 1 до 100", quantity)
		}
	}
}
