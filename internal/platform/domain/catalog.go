package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Пределы позиции, объявленные в партнёрском контракте. Длины считаются в знаках, а
	// не в байтах: в названиях кириллица, и байтовый предел обрезал бы русское название
	// вдвое раньше обещанного.
	externalIDMaxLength      = 200
	productNameMaxLength     = 200
	productDescriptionMaxLen = 2000
)

// CatalogItem — позиция меню, какой её отдало заведение: то, что площадка кладёт в свой
// каталог при синхронизации.
//
// Это не Product: у Product есть идентификатор, выданный площадкой, и заведение о нём не
// знает. Приехавшую позицию площадка узнаёт по внешнему идентификатору — и только по
// нему, — а собственный идентификатор выдаёт сама и больше не меняет.
type CatalogItem struct {
	// ExternalID — идентификатор позиции в системе заведения. Площадка сводит выгрузку со
	// своим каталогом по паре «заведение + этот идентификатор», поэтому он обязан быть
	// стабильным во времени: сменившийся означает, что позиция исчезла и появилась новая.
	ExternalID string
	// Name — название позиции, как его увидит покупатель.
	Name string
	// Description — описание позиции; пустое означает, что заведение его не дало.
	Description string
	// Price — цена позиции в копейках.
	Price Money
	// IsAvailable — ложь означает стоп-лист заведения: позиция видна, но заказать её нельзя.
	IsAvailable bool
}

// NewCatalogItem собирает приехавшую позицию, проверяя то, что о ней рассказало заведение.
//
// Проверка живёт в предметной области, а не в разборе ответа: пределы объявлены в
// контракте, но заведение — сторонняя система, и обещание контракта за неё никто не
// выполняет. Позиция, не прошедшая проверку, отменяет всю выгрузку целиком, а не
// выбрасывается по дороге: заведение, приславшее позицию без названия, ошиблось в
// выгрузке, и «почти всё меню» на витрине хуже прежнего.
func NewCatalogItem(externalID, name, description string, price Money, isAvailable bool) (CatalogItem, error) {
	// Пробелы по краям — след выгрузки, а не часть значения.
	externalID = strings.TrimSpace(externalID)
	name = strings.TrimSpace(name)

	switch {
	case utf8.RuneCountInString(externalID) < 1 || utf8.RuneCountInString(externalID) > externalIDMaxLength:
		return CatalogItem{}, BrokenMenuError{
			Reason: fmt.Sprintf("внешний идентификатор позиции: от 1 до %d знаков", externalIDMaxLength),
		}
	case utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > productNameMaxLength:
		return CatalogItem{}, BrokenMenuError{
			Reason: fmt.Sprintf("позиция %q: название от 1 до %d знаков", externalID, productNameMaxLength),
		}
	case utf8.RuneCountInString(description) > productDescriptionMaxLen:
		return CatalogItem{}, BrokenMenuError{
			Reason: fmt.Sprintf("позиция %q: описание не длиннее %d знаков", externalID, productDescriptionMaxLen),
		}
	case price < 0:
		return CatalogItem{}, BrokenMenuError{
			Reason: fmt.Sprintf("позиция %q: цена не может быть отрицательной", externalID),
		}
	}

	return CatalogItem{
		ExternalID:  externalID,
		Name:        name,
		Description: description,
		Price:       price,
		IsAvailable: isAvailable,
	}, nil
}

// BrokenMenuError — заведение отдало меню, которое площадка принять не может: позиция не
// прошла проверку, внешний идентификатор повторился, выгрузка не влезла в объявленные
// пределы.
//
// Ошибка одна на все эти случаи, потому что и последствие у них одно: синхронизация
// отменяется целиком, а прежний каталог остаётся нетронутым. Причина — текстом: её читает
// тот, кто чинит выгрузку заведения, и ветки поведения у площадки по ней нет.
type BrokenMenuError struct {
	// Reason — что именно не так, словами для того, кто чинит выгрузку.
	Reason string
}

// Error возвращает причину, по которой выгрузка не принята.
func (e BrokenMenuError) Error() string {
	return e.Reason
}
