package domain_test

import (
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// at — момент этого дня с точностью до минуты.
func at(hour, minute int) time.Time {
	return time.Date(2026, time.September, 8, hour, minute, 0, 0, time.UTC)
}

// hours задаёт часы работы или останавливает тест, если их не разобрать.
func hours(t *testing.T, opens, closes string) domain.WorkingHours {
	t.Helper()

	working, err := domain.NewWorkingHours(opens, closes)
	if err != nil {
		t.Fatalf("задать часы работы %s–%s: %v", opens, closes, err)
	}

	return working
}

func TestRestaurantWithoutAScheduleWorksRoundTheClock(t *testing.T) {
	t.Parallel()

	if !domain.AllDay().IsOpenAt(at(3, 0)) {
		t.Error("коробка без расписания закрыта ночью, а не сказать ей часов — не то же самое, что закрыть её")
	}
}

func TestEqualOpeningAndClosingTimeMeanRoundTheClockWork(t *testing.T) {
	t.Parallel()

	if !hours(t, "00:00", "00:00").IsOpenAt(at(4, 30)) {
		t.Error("одинаковые время открытия и закрытия закрыли заведение, ожидали круглосуточную работу")
	}
}

func TestRestaurantIsOpenFromTheOpeningMinuteAndClosedFromTheClosingOne(t *testing.T) {
	t.Parallel()

	working := hours(t, "10:00", "22:00")

	if !working.IsOpenAt(at(10, 0)) {
		t.Error("в минуту открытия заведение закрыто, а оно как раз открывается")
	}
	if working.IsOpenAt(at(22, 0)) {
		t.Error("в минуту закрытия заведение принимает заказ, а оно закрывается")
	}
	if working.IsOpenAt(at(9, 59)) {
		t.Error("заведение принимает заказ до открытия")
	}
}

func TestNightShiftSpansMidnight(t *testing.T) {
	t.Parallel()

	working := hours(t, "22:00", "02:00")

	if !working.IsOpenAt(at(23, 30)) {
		t.Error("ночная смена закрыта до полуночи")
	}
	if !working.IsOpenAt(at(1, 0)) {
		t.Error("ночная смена закрыта после полуночи")
	}
	if working.IsOpenAt(at(12, 0)) {
		t.Error("ночная смена открыта днём")
	}
}

func TestScheduleThatIsNotClockTimeIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewWorkingHours("утром", "22:00"); err == nil {
		t.Error("часы работы приняты не временем: служба поднялась бы с расписанием, которого не понимает")
	}
	if _, err := domain.NewWorkingHours("10:00", "24:00"); err == nil {
		t.Error("«24:00» принято временем закрытия, а круглосуточная работа записывается равными часами")
	}
}
