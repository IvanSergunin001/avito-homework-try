// Тесты предметной области идут без всякой инфраструктуры: ни базы, ни HTTP, ни
// подменённых зависимостей — правила приёма заказа и его рассмотрения не зависят ни от
// чего, кроме самих себя, и проверяются вызовом функции.
//
// Имя теста называет правило, а не функцию под тестом: по списку упавших тестов должно
// быть видно, какое правило коробки нарушено.
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// croissant — внешний идентификатор позиции, о которой идёт речь в тестах приёма.
const croissant = "018f2b5c-1f4a-7c3e-9a10-2b8f5d7c1a04"

// noon — момент, в который заведение точно работает: часы в тестах приёма круглосуточные,
// и время в них — просто отметка о приёме.
var noon = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

// menuOf собирает меню, о котором спрашивает заказ: ключ — внешний идентификатор позиции.
func menuOf(items ...domain.MenuItem) map[string]domain.MenuItem {
	menu := make(map[string]domain.MenuItem, len(items))
	for _, item := range items {
		menu[item.ExternalID()] = item
	}

	return menu
}

// availableItem — позиция меню с заданными ценой и остатком, лежащая по croissant.
func availableItem(t *testing.T, price domain.Money, stock int) domain.MenuItem {
	t.Helper()

	id, err := uuid.Parse(croissant)
	if err != nil {
		t.Fatalf("разобрать идентификатор позиции: %v", err)
	}

	return domain.MenuItem{
		ID:       id,
		MenuCard: domain.MenuCard{Name: "Круассан с миндалём", Price: price},
		Stock:    domain.Stock(stock),
	}
}

// orderFor собирает просьбу принять заказ на одну позицию по указанной цене.
func orderFor(t *testing.T, quantity int, price domain.Money) domain.OrderRequest {
	t.Helper()

	platformOrderID := uuid.New()
	items := []domain.RequestedItem{{ExternalID: croissant, Quantity: quantity, Price: price}}

	request, err := domain.NewOrderRequest(platformOrderID, platformOrderID,
		"Москва, Лесная 7, кв. 12", items, price*domain.Money(quantity), noon)
	if err != nil {
		t.Fatalf("собрать заказ: %v", err)
	}

	return request
}

// refusalFrom разбирает ошибку как отказ принять заказ.
func refusalFrom(t *testing.T, err error) domain.OrderRefusedError {
	t.Helper()

	var refusal domain.OrderRefusedError
	if !errors.As(err, &refusal) {
		t.Fatalf("ожидали отказ принять заказ, получили %v", err)
	}

	return refusal
}

func TestAcceptedOrderWaitsForTheOperatorDecision(t *testing.T) {
	t.Parallel()

	order, err := domain.AcceptOrder(uuid.New(), orderFor(t, 2, 45000),
		menuOf(availableItem(t, 45000, 5)), domain.AllDay(), noon)
	if err != nil {
		t.Fatalf("заказ не принят: %v", err)
	}

	if order.Status != domain.UnderReview {
		t.Errorf("статус принятого заказа %q, ожидали %q: решение за оператором",
			order.Status, domain.UnderReview)
	}
}

func TestAcceptedOrderKeepsTheLinkToThePlatformOrderAndTheIdempotencyKey(t *testing.T) {
	t.Parallel()

	request := orderFor(t, 1, 45000)

	order, err := domain.AcceptOrder(uuid.New(), request,
		menuOf(availableItem(t, 45000, 5)), domain.AllDay(), noon)
	if err != nil {
		t.Fatalf("заказ не принят: %v", err)
	}

	if order.PlatformOrderID != request.PlatformOrderID {
		t.Errorf("ссылка на заказ площадки %s, ожидали %s", order.PlatformOrderID, request.PlatformOrderID)
	}
	if order.IdempotencyKey != request.IdempotencyKey {
		t.Errorf("ключ идемпотентности %s, ожидали %s", order.IdempotencyKey, request.IdempotencyKey)
	}
	if order.ID == request.PlatformOrderID {
		t.Error("заказ коробки взял идентификатор заказа площадки: у коробки свой заказ и свой идентификатор")
	}
}

func TestAcceptedOrderRemembersNamesAndPricesOfTheMomentItWasAccepted(t *testing.T) {
	t.Parallel()

	order, err := domain.AcceptOrder(uuid.New(), orderFor(t, 3, 45000),
		menuOf(availableItem(t, 45000, 5)), domain.AllDay(), noon)
	if err != nil {
		t.Fatalf("заказ не принят: %v", err)
	}

	if len(order.Items) != 1 {
		t.Fatalf("в заказе %d позиций, ожидали одну", len(order.Items))
	}
	if order.Items[0].Name != "Круассан с миндалём" || order.Items[0].Price != 45000 {
		t.Errorf("позиция заказа %q по цене %d, ожидали снимок меню на момент приёма",
			order.Items[0].Name, order.Items[0].Price)
	}
	if order.Total != 135000 {
		t.Errorf("сумма заказа %d, ожидали 135000: коробка считает её по своим ценам", order.Total)
	}
}

func TestOrderForAnItemTheRestaurantDoesNotHaveInItsMenuIsRefusedAsUnknownProduct(t *testing.T) {
	t.Parallel()

	_, err := domain.AcceptOrder(uuid.New(), orderFor(t, 1, 45000),
		menuOf(), domain.AllDay(), noon)

	refusal := refusalFrom(t, err)
	if refusal.Reason != domain.UnknownProduct {
		t.Errorf("причина отказа %q, ожидали %q", refusal.Reason, domain.UnknownProduct)
	}
	if refusal.ProductExternalID != croissant {
		t.Errorf("отказ не называет позицию (%q), а площадке нужно знать, какую чинить",
			refusal.ProductExternalID)
	}
}

func TestOrderForAnItemInTheStopListIsRefusedAsOutOfStock(t *testing.T) {
	t.Parallel()

	item := availableItem(t, 45000, 5)
	item.InStopList = true

	_, err := domain.AcceptOrder(uuid.New(), orderFor(t, 1, 45000),
		menuOf(item), domain.AllDay(), noon)

	refusal := refusalFrom(t, err)
	if refusal.Reason != domain.OutOfStock {
		t.Errorf("причина отказа %q, ожидали %q", refusal.Reason, domain.OutOfStock)
	}
	if refusal.ProductExternalID != croissant {
		t.Errorf("отказ не называет позицию (%q)", refusal.ProductExternalID)
	}
}

func TestOrderForMoreUnitsThanAreLeftIsRefusedAsOutOfStock(t *testing.T) {
	t.Parallel()

	_, err := domain.AcceptOrder(uuid.New(), orderFor(t, 3, 45000),
		menuOf(availableItem(t, 45000, 2)), domain.AllDay(), noon)

	refusal := refusalFrom(t, err)
	if refusal.Reason != domain.OutOfStock {
		t.Errorf("причина отказа %q, ожидали %q", refusal.Reason, domain.OutOfStock)
	}
}

func TestOrderForExactlyWhatIsLeftIsAccepted(t *testing.T) {
	t.Parallel()

	if _, err := domain.AcceptOrder(uuid.New(), orderFor(t, 2, 45000),
		menuOf(availableItem(t, 45000, 2)), domain.AllDay(), noon); err != nil {
		t.Errorf("заказ на весь остаток не принят (%v), а продать последние порции заведение может", err)
	}
}

func TestPriceDifferentFromTheOneThePlatformShowedIsRefusedWithTheCurrentPrice(t *testing.T) {
	t.Parallel()

	_, err := domain.AcceptOrder(uuid.New(), orderFor(t, 1, 45000),
		menuOf(availableItem(t, 49000, 5)), domain.AllDay(), noon)

	refusal := refusalFrom(t, err)
	if refusal.Reason != domain.PriceChanged {
		t.Errorf("причина отказа %q, ожидали %q", refusal.Reason, domain.PriceChanged)
	}
	if refusal.CurrentPrice != 49000 {
		t.Errorf("отказ называет цену %d, ожидали действующую 49000: коробка — источник истины по цене",
			refusal.CurrentPrice)
	}
}

func TestClosedRestaurantRefusesTheOrderWithoutLookingAtWhatWasOrdered(t *testing.T) {
	t.Parallel()

	morning, err := domain.NewWorkingHours("10:00", "22:00")
	if err != nil {
		t.Fatalf("задать часы работы: %v", err)
	}

	night := time.Date(2026, time.September, 8, 3, 0, 0, 0, time.UTC)

	// Меню пустое: будь заведение открыто, отказ был бы о неизвестной позиции.
	_, err = domain.AcceptOrder(uuid.New(), orderFor(t, 1, 45000), menuOf(), morning, night)

	refusal := refusalFrom(t, err)
	if refusal.Reason != domain.RestaurantClosed {
		t.Errorf("причина отказа %q, ожидали %q", refusal.Reason, domain.RestaurantClosed)
	}
}

func TestOrderWhoseTotalDisagreesWithItsItemsIsNotAnOrderTheBoxCanRefuseOrAccept(t *testing.T) {
	t.Parallel()

	request := orderFor(t, 2, 45000)
	request.Total = 1

	_, err := domain.AcceptOrder(uuid.New(), request,
		menuOf(availableItem(t, 45000, 5)), domain.AllDay(), noon)

	var invalid domain.InvalidOrderError
	if !errors.As(err, &invalid) {
		t.Fatalf("ожидали, что заказ признают сломанным вызовом, получили %v", err)
	}
}

func TestOrderWithoutAddressIsNotAccepted(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	items := []domain.RequestedItem{{ExternalID: croissant, Quantity: 1, Price: 45000}}

	if _, err := domain.NewOrderRequest(id, id, "   ", items, 45000, noon); err == nil {
		t.Error("заказ без адреса принят, а доставлять его некуда")
	}
}

func TestOrderWithoutItemsIsNotAccepted(t *testing.T) {
	t.Parallel()

	id := uuid.New()

	if _, err := domain.NewOrderRequest(id, id, "Москва, Лесная 7", nil, 0, noon); err == nil {
		t.Error("пустой заказ принят, а готовить в нём нечего")
	}
}

func TestSameItemListedTwiceIsNotAnOrderTheBoxAccepts(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	items := []domain.RequestedItem{
		{ExternalID: croissant, Quantity: 2, Price: 45000},
		{ExternalID: croissant, Quantity: 2, Price: 45000},
	}

	if _, err := domain.NewOrderRequest(id, id, "Москва, Лесная 7", items, 180000, noon); err == nil {
		t.Error("одна позиция дважды в составе принята, а остаток проверялся бы по каждой строке отдельно")
	}
}

func TestOrderOfZeroUnitsIsNotAccepted(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	items := []domain.RequestedItem{{ExternalID: croissant, Quantity: 0, Price: 45000}}

	if _, err := domain.NewOrderRequest(id, id, "Москва, Лесная 7", items, 0, noon); err == nil {
		t.Error("заказ на ноль единиц принят, а заказывать в нём нечего")
	}
}

// underReview — принятый заказ, ждущий решения оператора.
func underReview(t *testing.T) domain.Order {
	t.Helper()

	order, err := domain.AcceptOrder(uuid.New(), orderFor(t, 1, 45000),
		menuOf(availableItem(t, 45000, 5)), domain.AllDay(), noon)
	if err != nil {
		t.Fatalf("заказ не принят: %v", err)
	}

	return order
}

func TestOperatorConfirmingTheOrderPutsItInWork(t *testing.T) {
	t.Parallel()

	order := underReview(t)
	if err := order.Confirm(); err != nil {
		t.Fatalf("оператор не смог подтвердить заказ на рассмотрении: %v", err)
	}

	if order.Status != domain.Confirmed {
		t.Errorf("статус подтверждённого заказа %q, ожидали %q", order.Status, domain.Confirmed)
	}
}

func TestOrderIsConfirmedOnlyWhileItIsUnderReview(t *testing.T) {
	t.Parallel()

	order := underReview(t)
	if err := order.Confirm(); err != nil {
		t.Fatalf("первое подтверждение не прошло: %v", err)
	}

	if err := order.Confirm(); !errors.Is(err, domain.ErrOrderNotUnderReview) {
		t.Errorf("повторное подтверждение вернуло %v, ожидали отказ: решение по заказу уже принято", err)
	}
}

func TestRejectedOrderCarriesTheReasonTheCustomerWillSee(t *testing.T) {
	t.Parallel()

	order := underReview(t)
	if err := order.Reject("Закончились продукты"); err != nil {
		t.Fatalf("оператор не смог отклонить заказ на рассмотрении: %v", err)
	}

	if order.Status != domain.Cancelled {
		t.Errorf("статус отклонённого заказа %q, ожидали %q", order.Status, domain.Cancelled)
	}
	if order.CancellationReason != "Закончились продукты" {
		t.Errorf("причина отказа %q, ожидали ту, которую назвал оператор", order.CancellationReason)
	}
}

func TestOrderIsNotRejectedWithoutAReason(t *testing.T) {
	t.Parallel()

	order := underReview(t)

	if err := order.Reject("   "); err == nil {
		t.Error("заказ отклонён без причины, а покупателю нечего показать")
	}
	if order.Status != domain.UnderReview {
		t.Errorf("статус заказа %q, ожидали %q: неудавшийся отказ ничего не меняет",
			order.Status, domain.UnderReview)
	}
}

func TestConfirmedOrderIsNoLongerRejectedAtReview(t *testing.T) {
	t.Parallel()

	order := underReview(t)
	if err := order.Confirm(); err != nil {
		t.Fatalf("подтверждение не прошло: %v", err)
	}

	if err := order.Reject("Закончились продукты"); !errors.Is(err, domain.ErrOrderNotUnderReview) {
		t.Errorf("подтверждённый заказ отклонён на этапе рассмотрения (%v), а он уже в работе", err)
	}
}

// inWork — заказ, за который оператор взялся: отсюда начинается его путь по кухне.
func inWork(t *testing.T) domain.Order {
	t.Helper()

	order := underReview(t)
	if err := order.Confirm(); err != nil {
		t.Fatalf("подтверждение не прошло: %v", err)
	}

	return order
}

// transitionFrom разбирает ошибку как отказ перехода.
func transitionFrom(t *testing.T, err error) domain.OrderTransitionError {
	t.Helper()

	var refusal domain.OrderTransitionError
	if !errors.As(err, &refusal) {
		t.Fatalf("ожидали отказ перехода, получили %v", err)
	}

	return refusal
}

func TestOrderGoesFromCookingThroughDeliveryToCompletion(t *testing.T) {
	t.Parallel()

	order := inWork(t)

	for _, next := range []domain.OrderStatus{domain.Cooking, domain.Delivering, domain.Completed} {
		if err := order.Advance(next); err != nil {
			t.Fatalf("заказ не перешёл в %q: %v", next, err)
		}
		if order.Status != next {
			t.Fatalf("статус заказа %q, ожидали %q", order.Status, next)
		}
	}
}

func TestOrderDoesNotSkipCookingOnItsWayToDelivery(t *testing.T) {
	t.Parallel()

	order := inWork(t)

	refusal := transitionFrom(t, order.Advance(domain.Delivering))
	if refusal.From != domain.Confirmed || refusal.To != domain.Delivering {
		t.Errorf("отказ описывает переход %q → %q, ожидали confirmed → delivering: оператору нужно видеть,"+
			" где заказ на самом деле", refusal.From, refusal.To)
	}
	if order.Status != domain.Confirmed {
		t.Errorf("статус заказа %q, ожидали confirmed: несостоявшийся переход ничего не меняет", order.Status)
	}
}

func TestOrderIsNotCookedBeforeTheOperatorTookIt(t *testing.T) {
	t.Parallel()

	order := underReview(t)

	if err := order.Advance(domain.Cooking); err == nil {
		t.Error("заказ ушёл в готовку, минуя решение оператора: заведение готовит то, за что не бралось")
	}
}

func TestCompletedOrderIsTheEndOfTheRoad(t *testing.T) {
	t.Parallel()

	order := inWork(t)
	for _, next := range []domain.OrderStatus{domain.Cooking, domain.Delivering, domain.Completed} {
		if err := order.Advance(next); err != nil {
			t.Fatalf("заказ не перешёл в %q: %v", next, err)
		}
	}

	if _, ok := order.Next(); ok {
		t.Error("у доставленного заказа нашёлся следующий шаг, а его путь пройден")
	}
	if err := order.Advance(domain.Cooking); err == nil {
		t.Error("доставленный заказ вернулся на кухню")
	}
}

func TestCancelledOrderIsNotMovedAnyFurther(t *testing.T) {
	t.Parallel()

	order := underReview(t)
	if err := order.Reject("Закончились продукты"); err != nil {
		t.Fatalf("отказ не прошёл: %v", err)
	}

	if _, ok := order.Next(); ok {
		t.Error("у отменённого заказа нашёлся следующий шаг: отмена — обрыв пути, а не ступень")
	}
	if err := order.Advance(domain.Cooking); err == nil {
		t.Error("отменённый заказ ушёл в готовку: заведение готовит то, от чего отказалось")
	}
}

func TestEveryStatusOnTheWayIsCountedAsStillInWork(t *testing.T) {
	t.Parallel()

	statuses := domain.StatusesInWork()

	expected := map[domain.OrderStatus]bool{
		domain.UnderReview: true, domain.Confirmed: true,
		domain.Cooking: true, domain.Delivering: true,
	}
	if len(statuses) != len(expected) {
		t.Fatalf("в работе %v, ожидали четыре незавершённых состояния", statuses)
	}
	for _, status := range statuses {
		if !expected[status] {
			t.Errorf("состояние %q названо незавершённым, а двигать его дальше некуда", status)
		}
	}
}
