// Приём уведомлений заведения — часть предметной области, а не обработчика, поэтому и
// проверяется он без инфраструктуры: ни базы, ни HTTP, ни подменённых зависимостей.
//
// Имя теста называет правило, а не функцию под тестом: по списку упавших тестов должно
// быть видно, какое правило площадки нарушено.
package domain_test

import (
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// notifiedAt — момент, которым площадка отмечает переход, выполненный по уведомлению.
var notifiedAt = placedAt.Add(time.Minute)

// notify передаёт заказу уведомление заведения и требует, чтобы оно было принято.
func notify(t *testing.T, order *domain.Order, status domain.OrderStatus) bool {
	t.Helper()

	applied, err := order.ApplyNotification(status, "", notifiedAt)
	if err != nil {
		t.Fatalf("уведомление о %q не принято: %v", status, err)
	}

	return applied
}

func TestOrderMovesToTheStatusTheRestaurantNotifiesAbout(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.UnderReview)

	if !notify(t, &order, domain.Confirmed) {
		t.Fatal("уведомление о новом состоянии ничего не изменило")
	}

	if order.Status != domain.Confirmed {
		t.Errorf("статус заказа %q, ожидали %q: заказ двигают уведомления заведения",
			order.Status, domain.Confirmed)
	}
	if !order.UpdatedAt.Equal(notifiedAt) {
		t.Errorf("время изменения %s, ожидали %s", order.UpdatedAt, notifiedAt)
	}
}

func TestNotificationAboutAStatusTheOrderHasAlreadyPassedChangesNothing(t *testing.T) {
	t.Parallel()

	// Уведомления доставляются с повторами и приезжают вперемешку: заказ, который уже
	// везут, не возвращается к готовке от опоздавшего «готовим».
	for _, late := range []domain.OrderStatus{domain.Confirmed, domain.Cooking} {
		order := orderIn(t, domain.Delivering)

		if notify(t, &order, late) {
			t.Errorf("уведомление о пройденном %q изменило заказ", late)
		}
		if order.Status != domain.Delivering {
			t.Errorf("статус заказа %q, ожидали %q: заказ не возвращается к прошлому состоянию",
				order.Status, domain.Delivering)
		}
	}
}

func TestRepeatedNotificationAboutTheStatusTheOrderIsAlreadyInChangesNothing(t *testing.T) {
	t.Parallel()

	// Ровно это и есть защита от ретраев: заведение повторяет уведомление, пока не
	// получит ответ, и повтор обязан быть безобидным.
	for _, status := range []domain.OrderStatus{domain.Confirmed, domain.Cooking, domain.Delivering} {
		order := orderIn(t, status)

		if notify(t, &order, status) {
			t.Errorf("повторное уведомление о %q изменило заказ", status)
		}
		if order.Status != status {
			t.Errorf("статус заказа %q, ожидали %q", order.Status, status)
		}
	}
}

func TestNotificationAboutATerminalOrderIsAcceptedAndChangesNothing(t *testing.T) {
	t.Parallel()

	for _, terminal := range []domain.OrderStatus{domain.Completed, domain.Cancelled} {
		for _, status := range statuses {
			order := orderIn(t, terminal)

			applied, err := order.ApplyNotification(status, "Закончились продукты", notifiedAt)
			if err != nil {
				t.Errorf("уведомление о %q для заказа в %q отклонено: %v — заказ прожил свою жизнь, "+
					"и повторять уведомление заведению незачем", status, terminal, err)

				continue
			}
			if applied {
				t.Errorf("уведомление о %q изменило заказ в терминальном %q", status, terminal)
			}
			if order.Status != terminal {
				t.Errorf("статус заказа %q, ожидали %q", order.Status, terminal)
			}
		}
	}
}

func TestNotificationThatSkipsAStatusIsRefusedByTheOrderItself(t *testing.T) {
	t.Parallel()

	// Заказ, о доставке которого сообщают раньше, чем о начале готовки, — это не
	// опоздавшее уведомление, а потерянное: принять его значило бы согласиться с
	// историей, которой не было.
	skipping := []struct {
		from domain.OrderStatus
		to   domain.OrderStatus
	}{
		{domain.Pending, domain.Confirmed},
		{domain.UnderReview, domain.Cooking},
		{domain.UnderReview, domain.Delivering},
		{domain.Confirmed, domain.Completed},
	}

	for _, notification := range skipping {
		order := orderIn(t, notification.from)

		applied, err := order.ApplyNotification(notification.to, "", notifiedAt)
		if err == nil {
			t.Errorf("уведомление о %q для заказа в %q принято (изменение: %t), а не должно",
				notification.to, notification.from, applied)

			continue
		}

		refusal := refusedTransition(t, err)
		if refusal.From != notification.from || refusal.To != notification.to {
			t.Errorf("отказ описывает переход %s → %s, ожидали %s → %s",
				refusal.From, refusal.To, notification.from, notification.to)
		}
		if refusal.By != domain.ByRestaurant {
			t.Errorf("отказ описывает требование %s, ожидали %s: уведомляет заведение",
				refusal.By, domain.ByRestaurant)
		}
		if order.Status != notification.from {
			t.Errorf("отказанное уведомление всё же изменило статус на %q", order.Status)
		}
	}
}

func TestRestaurantCancelsTheOrderByNotificationUntilItStartsCooking(t *testing.T) {
	t.Parallel()

	for _, status := range []domain.OrderStatus{domain.UnderReview, domain.Confirmed} {
		order := orderIn(t, status)

		applied, err := order.ApplyNotification(domain.Cancelled, "Закончились продукты", notifiedAt)
		if err != nil {
			t.Errorf("заведение не отменило заказ в состоянии %q: %v", status, err)

			continue
		}
		if !applied {
			t.Errorf("отмена заказа в состоянии %q ничего не изменила", status)
		}
		if order.Status != domain.Cancelled {
			t.Errorf("статус заказа %q, ожидали %q", order.Status, domain.Cancelled)
		}
		if order.CancelledBy != domain.ByRestaurant {
			t.Errorf("инициатор отмены %q, ожидали %q", order.CancelledBy, domain.ByRestaurant)
		}
		if order.CancellationReason != "Закончились продукты" {
			t.Errorf("причина отмены %q: её показывают покупателю", order.CancellationReason)
		}
	}
}

func TestCancellationNotificationWithoutAReasonIsRefused(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Confirmed)

	if _, err := order.ApplyNotification(domain.Cancelled, "   ", notifiedAt); err == nil {
		t.Fatal("заказ отменён без причины: причину показывают покупателю")
	}

	if order.Status != domain.Confirmed {
		t.Errorf("статус заказа %q, ожидали %q: отказанная отмена не меняет заказ",
			order.Status, domain.Confirmed)
	}
}

func TestRestaurantThatStartedCookingCancelsTheOrderThroughSupportAndNotByNotification(t *testing.T) {
	t.Parallel()

	for _, status := range []domain.OrderStatus{domain.Cooking, domain.Delivering} {
		order := orderIn(t, status)

		if _, err := order.ApplyNotification(domain.Cancelled, "Закончились продукты", notifiedAt); err == nil {
			t.Errorf("заведение отменило заказ в состоянии %q уведомлением: продукты потрачены, "+
				"и такой случай разбирает саппорт", status)

			continue
		}
		if order.Status != status {
			t.Errorf("статус заказа %q, ожидали %q", order.Status, status)
		}
	}
}
