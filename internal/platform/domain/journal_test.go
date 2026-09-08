// Журнал состояний заказа проверяется там же, где живёт, — в предметной области и без
// инфраструктуры: записывает его сам агрегат, и вопрос «попал ли переход в историю»
// задаётся к нему, а не к хранилищу.
package domain_test

import (
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

func TestJournalOfTheOrderBeginsWithTheMomentItWasPlaced(t *testing.T) {
	t.Parallel()

	order := place(t, croissant(1, 45000))

	changes := order.StatusChanges()
	if len(changes) != 1 {
		t.Fatalf("в журнале оформленного заказа %d записей, ожидали одну: заказ появился", len(changes))
	}

	if changes[0].Status != domain.Pending {
		t.Errorf("первая запись журнала о %q, ожидали %q", changes[0].Status, domain.Pending)
	}
	if changes[0].Actor != domain.ByCustomer {
		t.Errorf("инициатор первой записи %q, ожидали %q: заказ оформил покупатель",
			changes[0].Actor, domain.ByCustomer)
	}
	if !changes[0].OccurredAt.Equal(placedAt) {
		t.Errorf("время первой записи %s, ожидали %s", changes[0].OccurredAt, placedAt)
	}
}

func TestEveryStatusChangeIsWrittenToTheJournalWithItsActorAndTime(t *testing.T) {
	t.Parallel()

	order := place(t, croissant(1, 45000))

	path := []struct {
		status domain.OrderStatus
		by     domain.Actor
		at     time.Time
	}{
		{domain.UnderReview, domain.ByRestaurant, placedAt.Add(time.Second)},
		{domain.Confirmed, domain.ByRestaurant, placedAt.Add(time.Minute)},
		{domain.Cooking, domain.ByRestaurant, placedAt.Add(2 * time.Minute)},
	}

	for _, step := range path {
		if err := order.TransitionTo(step.status, step.by, step.at); err != nil {
			t.Fatalf("переход в %q не выполнен: %v", step.status, err)
		}
	}

	changes := order.StatusChanges()
	if len(changes) != len(path)+1 {
		t.Fatalf("в журнале %d записей, ожидали %d: оформление и каждый переход",
			len(changes), len(path)+1)
	}

	for index, step := range path {
		change := changes[index+1]

		switch {
		case change.Status != step.status:
			t.Errorf("запись %d о переходе в %q, ожидали %q", index+1, change.Status, step.status)
		case change.Actor != step.by:
			t.Errorf("инициатор записи о %q — %q, ожидали %q", step.status, change.Actor, step.by)
		case !change.OccurredAt.Equal(step.at):
			t.Errorf("время записи о %q — %s, ожидали %s", step.status, change.OccurredAt, step.at)
		}
	}
}

func TestCancellationIsWrittenToTheJournalWithTheActorWhoAskedForIt(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Cooking)
	cancelledAt := placedAt.Add(time.Hour)

	if err := order.Cancel(domain.BySupport, "Заведение не берёт трубку", cancelledAt); err != nil {
		t.Fatalf("отмена не выполнена: %v", err)
	}

	changes := order.StatusChanges()
	last := changes[len(changes)-1]

	if last.Status != domain.Cancelled {
		t.Errorf("последняя запись журнала о %q, ожидали %q", last.Status, domain.Cancelled)
	}
	if last.Actor != domain.BySupport {
		t.Errorf("инициатор последней записи %q, ожидали %q: отменял саппорт",
			last.Actor, domain.BySupport)
	}
	if !last.OccurredAt.Equal(cancelledAt) {
		t.Errorf("время последней записи %s, ожидали %s", last.OccurredAt, cancelledAt)
	}
}

func TestRefusedTransitionLeavesNoTraceInTheJournal(t *testing.T) {
	t.Parallel()

	order := orderIn(t, domain.Cooking)
	before := len(order.StatusChanges())

	if err := order.TransitionTo(domain.Completed, domain.ByRestaurant, placedAt); err == nil {
		t.Fatal("заказ ушёл из готовки сразу в завершённые")
	}

	if after := len(order.StatusChanges()); after != before {
		t.Errorf("в журнале %d записей, было %d: отказанный переход не случился, и записывать нечего",
			after, before)
	}
}

func TestNotificationThatChangedNothingLeavesNoTraceInTheJournal(t *testing.T) {
	t.Parallel()

	// Повтор уведомления не должен удваивать историю заказа: журнал отвечает на вопрос,
	// что с заказом происходило, а не сколько раз заведение об этом рассказало.
	order := orderIn(t, domain.Delivering)
	before := len(order.StatusChanges())

	for range 3 {
		if _, err := order.ApplyNotification(domain.Cooking, "", notifiedAt); err != nil {
			t.Fatalf("опоздавшее уведомление отклонено: %v", err)
		}
	}

	if after := len(order.StatusChanges()); after != before {
		t.Errorf("в журнале %d записей, было %d: опоздавшее уведомление ничего не изменило",
			after, before)
	}
}

func TestOrderReadFromStorageCarriesNoChangesUntilItMovesAgain(t *testing.T) {
	t.Parallel()

	// Так заказ выглядит после чтения из хранилища: его прошлые переходы записаны в
	// журнал тогда, когда случились, и записывать их второй раз — удваивать историю.
	order := domain.Order{Status: domain.Confirmed}

	if changes := order.StatusChanges(); len(changes) != 0 {
		t.Fatalf("прочитанный заказ несёт %d записей журнала, ожидали ни одной", len(changes))
	}

	if err := order.TransitionTo(domain.Cooking, domain.ByRestaurant, notifiedAt); err != nil {
		t.Fatalf("переход не выполнен: %v", err)
	}

	if changes := order.StatusChanges(); len(changes) != 1 {
		t.Errorf("после перехода в журнале %d записей, ожидали одну — сам переход", len(changes))
	}
}
