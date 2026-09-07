package httpx

import (
	"errors"
	"fmt"
)

// Paging — окно выдачи, приведённое к числам: значения по умолчанию подставлены,
// границы проверены.
type Paging struct {
	// Limit — сколько записей отдавать на страницу.
	Limit int
	// Offset — сколько записей пропустить от начала выдачи.
	Offset int
}

// ResolvePaging разбирает параметры страницы: отсутствующие заменяет умолчаниями,
// выходящие за объявленные контрактом границы отклоняет.
//
// Проверка живёт здесь, а не в спецификации: кодогенератор разбирает параметры, но не
// сверяет их с `minimum` и `maximum`. Отдать страницу больше объявленной в контракте
// нельзя — клиент вправе рассчитывать на потолок, а служба на предсказуемый объём выдачи.
func ResolvePaging(limit, offset *int, defaultLimit, maxLimit int) (Paging, error) {
	paging := Paging{Limit: defaultLimit}

	if limit != nil {
		paging.Limit = *limit
	}
	if paging.Limit < 1 || paging.Limit > maxLimit {
		return Paging{}, fmt.Errorf("limit: допустимы значения от 1 до %d", maxLimit)
	}

	if offset != nil {
		paging.Offset = *offset
	}
	if paging.Offset < 0 {
		return Paging{}, errors.New("offset: допустимы значения не меньше 0")
	}

	return paging, nil
}
