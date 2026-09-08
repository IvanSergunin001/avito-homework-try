package domain

import (
	"fmt"
	"time"
)

// clockLayout — как записаны часы работы: «ЧЧ:ММ» местного времени заведения.
const clockLayout = "15:04"

// WorkingHours — часы, в которые заведение принимает заказы.
//
// Расписание живёт настройкой заведения, а не признаком в базе: «открыто» — это не то,
// что кто-то переключает руками каждое утро, а то, что следует из времени суток.
// Переключатель пришлось бы возвращать в положение «открыто» вручную, и забытый на ночь
// он означал бы заказы, которые некому готовить.
//
// Значение по умолчанию — круглосуточная работа: коробка, которой не сказали часов,
// принимает заказы всегда, а не отказывает во всех подряд.
type WorkingHours struct {
	// opens и closes — время от полуночи. Одинаковые значения означают круглосуточную
	// работу: сутки без единой минуты перерыва естественнее описывать так, чем
	// невыразимым в «ЧЧ:ММ» временем закрытия «24:00».
	opens  time.Duration
	closes time.Duration
}

// AllDay — заведение работает круглосуточно.
func AllDay() WorkingHours {
	return WorkingHours{}
}

// NewWorkingHours разбирает часы работы, записанные как «ЧЧ:ММ»: например, «10:00» и
// «23:00». Время закрытия раньше времени открытия описывает смену через полночь
// («22:00»–«02:00») — так работает ночная пекарня, и запрещать ей это незачем.
func NewWorkingHours(opens, closes string) (WorkingHours, error) {
	from, err := parseClock(opens)
	if err != nil {
		return WorkingHours{}, err
	}

	until, err := parseClock(closes)
	if err != nil {
		return WorkingHours{}, err
	}

	return WorkingHours{opens: from, closes: until}, nil
}

// IsOpenAt отвечает, принимает ли заведение заказы в этот момент.
//
// Секунды в расчёт не идут: расписание заведения — это часы и минуты, и заказ, пришедший
// в «22:00:30», для закрывшегося в 22:00 заведения такой же поздний, как и любой другой.
func (h WorkingHours) IsOpenAt(t time.Time) bool {
	if h.opens == h.closes {
		return true
	}

	since := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute

	// Момент открытия принадлежит рабочему дню, момент закрытия — уже нет: в «22:00»
	// заведение закрывается, а не принимает последний заказ.
	if h.opens < h.closes {
		return since >= h.opens && since < h.closes
	}

	// Смена через полночь: рабочее время — это всё, что после открытия либо до закрытия.
	return since >= h.opens || since < h.closes
}

// String возвращает часы работы в том же виде, в каком их задали: строку видно в логе
// службы на старте, и она должна читаться так же, как настройка, из которой получена.
func (h WorkingHours) String() string {
	if h.opens == h.closes {
		return "круглосуточно"
	}

	return formatClock(h.opens) + "–" + formatClock(h.closes)
}

// parseClock разбирает «ЧЧ:ММ» во время от полуночи.
func parseClock(value string) (time.Duration, error) {
	parsed, err := time.Parse(clockLayout, value)
	if err != nil {
		return 0, fmt.Errorf("часы работы: %q — не время вида ЧЧ:ММ", value)
	}

	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, nil
}

// formatClock печатает время от полуночи как «ЧЧ:ММ».
func formatClock(since time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(since.Hours()), int(since.Minutes())%int(time.Hour/time.Minute))
}
