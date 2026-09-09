// Тесты предметной области идут без моков и без базы: правила меню ни от хранилища, ни от
// транспорта не зависят, и проверять их через шов значило бы проверять заодно роутер.
//
// Тест внутренний (`package domain`) намеренно: пределы он берёт из тех же констант, что и
// правило под тестом. Переписанные в тест руками числа не пиннили бы предел, а заводили бы
// ему четвёртую копию — к спецификациям и `CHECK` миграции.
package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestMenuCardIsRefusedWhenTheOperatorDescribedItWrong(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		name        string
		description string
		price       Money
	}{
		"без названия":               {name: "", price: 1},
		"название из одних пробелов": {name: "   ", price: 1},
		"название длиннее предела":   {name: strings.Repeat("я", nameMaxLength+1), price: 1},
		"описание длиннее предела":   {name: "Борщ", description: strings.Repeat("я", descriptionMaxLength+1), price: 1},
		"отрицательная цена":         {name: "Борщ", price: -1},
	}

	for reason, card := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			_, err := NewMenuCard(card.name, card.description, card.price)

			var invalid InvalidMenuItemError
			if !errors.As(err, &invalid) {
				t.Fatalf("карточка принята с ошибкой %v, ожидали отказ: %s", err, reason)
			}
			if invalid.Reason == "" {
				t.Error("в отказе нет причины, а её читает оператор, который правит форму")
			}
		})
	}
}

func TestMenuCardIsAcceptedRightUpToTheDeclaredLimits(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		name        string
		description string
		price       Money
	}{
		"название длиной ровно в предел": {name: strings.Repeat("я", nameMaxLength), price: 1},
		"описание длиной ровно в предел": {name: "Борщ", description: strings.Repeat("я", descriptionMaxLength)},
		"бесплатная позиция":             {name: "Хлеб к супу", price: 0},
	}

	for reason, card := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			if _, err := NewMenuCard(card.name, card.description, card.price); err != nil {
				t.Errorf("карточка отклонена (%v), ожидали, что пройдёт: %s — предел объявлен "+
					"включительно, и позиция ровно на нём законна", err, reason)
			}
		})
	}
}

func TestNameOfMenuCardLosesTheSpacesTypedAroundIt(t *testing.T) {
	t.Parallel()

	card, err := NewMenuCard("  Сырники  ", "", 25000)
	if err != nil {
		t.Fatalf("карточка отклонена: %v", err)
	}

	if card.Name != "Сырники" {
		t.Errorf("название сохранено как %q, ожидали «Сырники»: пробелы по краям — след "+
			"ввода, а не часть названия", card.Name)
	}
}

func TestStockIsAcceptedOnlyWithinWhatTheBoxCanKeep(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		count    int
		accepted bool
	}{
		"отрицательный остаток":   {count: -1},
		"позиция кончилась":       {count: 0, accepted: true},
		"остаток ровно в предел":  {count: stockMax, accepted: true},
		"остаток сверх предела":   {count: stockMax + 1},
		"опечатка в целый резерв": {count: 3_000_000_000},
	}

	for reason, want := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			stock, err := NewStock(want.count)

			switch {
			case want.accepted && err != nil:
				t.Errorf("остаток %d отклонён (%v), ожидали, что пройдёт", want.count, err)
			case want.accepted && int(stock) != want.count:
				t.Errorf("остаток сохранён как %d, ожидали %d", stock, want.count)
			case !want.accepted:
				var invalid InvalidMenuItemError
				if !errors.As(err, &invalid) {
					t.Errorf("остаток %d принят, ожидали отказ: %s", want.count, reason)
				}
			}
		})
	}
}

func TestItemIsAvailableOnlyWhenNothingStandsInTheWay(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		item MenuItem
		want bool
	}{
		"есть и не в стопе":   {item: MenuItem{Stock: 1}, want: true},
		"в стопе, хотя есть":  {item: MenuItem{Stock: 1, InStopList: true}},
		"кончилась":           {item: MenuItem{Stock: 0}},
		"кончилась и в стопе": {item: MenuItem{Stock: 0, InStopList: true}},
	}

	for reason, want := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			if got := want.item.IsAvailable(); got != want.want {
				t.Errorf("доступность %v, ожидали %v: %s", got, want.want, reason)
			}
		})
	}
}
