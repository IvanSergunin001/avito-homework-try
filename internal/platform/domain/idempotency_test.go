// Идемпотентность оформления — правило предметной области, и проверяется оно без всякой
// инфраструктуры: ни базы, ни HTTP. Уникальность строки в базе отбивает второй заказ, но
// решает, что означает занятый идентификатор — повтор или сломанный клиент, — только
// агрегат.
//
// Имя теста называет правило, а не функцию под тестом: по списку упавших тестов должно
// быть видно, какое правило площадки нарушено.
package domain_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// basket — корзина покупателя: товар и сколько его взяли. Цены здесь нет намеренно —
// тесты идемпотентности говорят о том, что прислал клиент, а цену подставляет площадка.
type basket struct {
	product  uuid.UUID
	quantity int
}

// order оформляет заказ так, как его оформляет площадка: цену каждой позиции берёт из
// каталога она сама, поэтому здесь цена — просто число, которое тест называет, когда
// хочет показать, что от него ничего не зависит.
func order(t *testing.T, id, customer, restaurant uuid.UUID, address string, price domain.Money, cart ...basket) domain.Order {
	t.Helper()

	items := make([]domain.OrderItem, 0, len(cart))
	for _, line := range cart {
		items = append(items, domain.OrderItem{
			ProductID:  line.product,
			ExternalID: line.product.String(),
			Name:       "Позиция " + line.product.String(),
			Price:      price,
			Quantity:   line.quantity,
		})
	}

	placed, err := domain.PlaceOrder(id, customer, restaurant, address, items, placedAt)
	if err != nil {
		t.Fatalf("заказ не оформлен: %v", err)
	}

	return placed
}

func TestRepeatedOrderWithTheSameBasketIsTheOrderThePlatformAlreadyHas(t *testing.T) {
	t.Parallel()

	id, customer, restaurant := uuid.New(), uuid.New(), uuid.New()
	croissant, latte := uuid.New(), uuid.New()

	stored := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 45000,
		basket{product: croissant, quantity: 2}, basket{product: latte, quantity: 1})
	again := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 45000,
		basket{product: croissant, quantity: 2}, basket{product: latte, quantity: 1})

	repeated, err := stored.Repeat(again)
	if err != nil {
		t.Fatalf("повтор того же заказа отклонён: %v", err)
	}
	if repeated.ID != stored.ID || repeated.CreatedAt != stored.CreatedAt {
		t.Errorf("повтор отдал заказ %s от %s, ожидали уже записанный %s от %s",
			repeated.ID, repeated.CreatedAt, stored.ID, stored.CreatedAt)
	}
}

func TestBasketRearrangedByTheClientIsStillTheSameOrder(t *testing.T) {
	t.Parallel()

	id, customer, restaurant := uuid.New(), uuid.New(), uuid.New()
	croissant, latte := uuid.New(), uuid.New()

	// Корзина — набор, а не список: клиент, отправивший те же позиции в другом порядке,
	// повторяет свой заказ, а не оформляет новый.
	stored := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 45000,
		basket{product: croissant, quantity: 2}, basket{product: latte, quantity: 1})
	again := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 45000,
		basket{product: latte, quantity: 1}, basket{product: croissant, quantity: 2})

	if _, err := stored.Repeat(again); err != nil {
		t.Errorf("переставленная местами корзина принята за другой заказ: %v", err)
	}
}

func TestPriceRaisedInTheCatalogDoesNotTurnTheRepeatIntoADifferentOrder(t *testing.T) {
	t.Parallel()

	id, customer, restaurant := uuid.New(), uuid.New(), uuid.New()
	croissant := uuid.New()

	// Цену подставляет площадка, а не клиент: заказ, повторённый после подорожания, — это
	// тот же самый запрос, и упираться в «ключ переиспользован» ему не за что.
	stored := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 45000,
		basket{product: croissant, quantity: 2})
	again := order(t, id, customer, restaurant, "Москва, Лесная 7, кв. 12", 90000,
		basket{product: croissant, quantity: 2})

	if _, err := stored.Repeat(again); err != nil {
		t.Errorf("подорожавшая позиция сделала повтор другим заказом: %v", err)
	}
}

func TestSameOrderIdWithADifferentBasketIsRefusedAsKeyReuse(t *testing.T) {
	t.Parallel()

	id, customer, restaurant := uuid.New(), uuid.New(), uuid.New()
	croissant, latte := uuid.New(), uuid.New()
	address := "Москва, Лесная 7, кв. 12"

	stored := order(t, id, customer, restaurant, address, 45000, basket{product: croissant, quantity: 2})

	others := map[string]domain.Order{
		"другое количество": order(t, id, customer, restaurant, address, 45000,
			basket{product: croissant, quantity: 3}),
		"другая позиция": order(t, id, customer, restaurant, address, 45000,
			basket{product: latte, quantity: 2}),
		"позиция сверх той же корзины": order(t, id, customer, restaurant, address, 45000,
			basket{product: croissant, quantity: 2}, basket{product: latte, quantity: 1}),
		"другой адрес": order(t, id, customer, restaurant, "Москва, Тверская 1", 45000,
			basket{product: croissant, quantity: 2}),
		"другое заведение": order(t, id, customer, uuid.New(), address, 45000,
			basket{product: croissant, quantity: 2}),
	}

	for name, other := range others {
		if _, err := stored.Repeat(other); !errors.Is(err, domain.ErrIdempotencyKeyReuse) {
			t.Errorf("%s под тем же идентификатором: %v, ожидали переиспользование ключа", name, err)
		}
	}
}

func TestOrderIdOfAnotherCustomerIsRefusedBeforeItsBasketIsEvenLookedAt(t *testing.T) {
	t.Parallel()

	id, restaurant := uuid.New(), uuid.New()
	croissant := uuid.New()
	address := "Москва, Лесная 7, кв. 12"

	stored := order(t, id, uuid.New(), restaurant, address, 45000, basket{product: croissant, quantity: 2})

	// Состав в обоих случаях разный ровно настолько, насколько он разный у двух чужих
	// друг другу заказов: по коду отказа не должно быть видно, совпал ли он.
	sameBasket := order(t, id, uuid.New(), restaurant, address, 45000, basket{product: croissant, quantity: 2})
	otherBasket := order(t, id, uuid.New(), restaurant, address, 45000, basket{product: uuid.New(), quantity: 1})

	for name, foreign := range map[string]domain.Order{
		"с тем же составом": sameBasket,
		"с другим составом": otherBasket,
	} {
		if _, err := stored.Repeat(foreign); !errors.Is(err, domain.ErrOrderIDTaken) {
			t.Errorf("чужой идентификатор %s: %v, ожидали занятый идентификатор", name, err)
		}
	}
}

func TestOrderKnowsWhichOfItsProductsTheRestaurantCallsByItsOwnIdentifier(t *testing.T) {
	t.Parallel()

	// Позицию, из-за которой заведение отказало, оно называет своим идентификатором:
	// узнать по нему товар площадки может только сам заказ — он уехал заведению снимком.
	placed := place(t, croissant(2, 45000), latte(1, 29900))

	if got := placed.ProductOf("latte-300"); got != placed.Items[1].ProductID {
		t.Errorf("позиции latte-300 отвечает товар %s, ожидали %s", got, placed.Items[1].ProductID)
	}
	if got := placed.ProductOf("тартар"); got != uuid.Nil {
		t.Errorf("позиции, которой в заказе нет, отвечает товар %s, ожидали пустой", got)
	}
}
