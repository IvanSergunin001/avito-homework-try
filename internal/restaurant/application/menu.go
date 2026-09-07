// Package application — сценарии коробки заведения: то, что делает коробка, выраженное
// в терминах предметной области и не знающее ни о HTTP, ни о SQL.
package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// Window — окно выдачи: сколько записей пропустить и сколько отдать. Границы окна
// проверяет тот, кто принимает запрос, — сценарию оно приходит уже приведённым к числам.
type Window struct {
	// Limit — сколько позиций отдавать на страницу.
	Limit int
	// Offset — сколько позиций пропустить от начала выдачи.
	Offset int
}

// MenuPage — окно меню вместе с общим числом позиций: без него ни площадка, ни оператор
// не знают, есть ли что листать дальше.
type MenuPage struct {
	// Items — позиции запрошенного окна.
	Items []domain.MenuItem
	// Total — сколько всего позиций в меню, а не сколько попало в окно.
	Total int64
}

// MenuItems — хранилище меню коробки.
//
// Правки возвращают позицию целиком, а не просто сообщают об успехе: оператор должен
// увидеть последствие своей правки — прежде всего доступность, которая складывается из
// остатка и стоп-листа, — а второй запрос за ней прочитал бы уже другое состояние.
type MenuItems interface {
	// Add заводит новую позицию.
	Add(ctx context.Context, item domain.MenuItem) error
	// Page отдаёт окно меню и общее число позиций.
	Page(ctx context.Context, window Window) (MenuPage, error)
	// UpdateCard заменяет карточку позиции. Если позиции нет — domain.ErrMenuItemNotFound.
	UpdateCard(ctx context.Context, id uuid.UUID, card domain.MenuCard) (domain.MenuItem, error)
	// SetStopListMark выставляет или снимает отметку стоп-листа. Если позиции нет —
	// domain.ErrMenuItemNotFound.
	SetStopListMark(ctx context.Context, id uuid.UUID, inStopList bool) (domain.MenuItem, error)
	// SetStock записывает остаток позиции. Если позиции нет — domain.ErrMenuItemNotFound.
	SetStock(ctx context.Context, id uuid.UUID, stock domain.Stock) (domain.MenuItem, error)
	// Remove удаляет позицию. Если позиции нет — domain.ErrMenuItemNotFound.
	Remove(ctx context.Context, id uuid.UUID) error
}

// Menu — меню коробки: то, чем оператор заведения без своих систем заменяет свой бэкенд,
// и то, что коробка отдаёт площадке по партнёрскому контракту.
//
// Сценарий один на обе поверхности намеренно: меню у заведения одно, и расхождение между
// тем, что видит оператор, и тем, что уезжает площадке, было бы дефектом, а не свойством.
type Menu struct {
	items MenuItems
}

// NewMenu собирает меню над хранилищем позиций.
func NewMenu(items MenuItems) *Menu {
	return &Menu{items: items}
}

// Add заводит позицию меню и возвращает её вместе с выданным идентификатором.
//
// Идентификатор выдаёт коробка, а не оператор: он уезжает площадке внешним
// идентификатором позиции и обязан быть стабильным, а придуманный руками рано или поздно
// повторится. uuid v7 монотонный — клиентский первичный ключ не разносит вставки по индексу.
func (m *Menu) Add(ctx context.Context, card domain.MenuCard, stock domain.Stock) (domain.MenuItem, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return domain.MenuItem{}, fmt.Errorf("выдать идентификатор позиции: %w", err)
	}

	item := domain.MenuItem{ID: id, MenuCard: card, Stock: stock}
	if err := m.items.Add(ctx, item); err != nil {
		return domain.MenuItem{}, err
	}

	return item, nil
}

// Page отдаёт страницу меню.
func (m *Menu) Page(ctx context.Context, window Window) (MenuPage, error) {
	return m.items.Page(ctx, window)
}

// Edit заменяет карточку позиции: название, описание и цену.
func (m *Menu) Edit(ctx context.Context, id uuid.UUID, card domain.MenuCard) (domain.MenuItem, error) {
	return m.items.UpdateCard(ctx, id, card)
}

// MarkStopList отмечает позицию как отсутствующую или снимает отметку.
//
// Отметка и её снятие — одна операция: состояние у позиции одно, и повторный вызов с тем
// же значением ничего не меняет — оператор, нажавший дважды, не получает отказа.
func (m *Menu) MarkStopList(ctx context.Context, id uuid.UUID, inStopList bool) (domain.MenuItem, error) {
	return m.items.SetStopListMark(ctx, id, inStopList)
}

// SetStock записывает остаток позиции целиком, а не приращением: оператор пересчитал и
// сообщил, сколько осталось, — повтор запроса по таймауту не должен списать порцию дважды.
func (m *Menu) SetStock(ctx context.Context, id uuid.UUID, stock domain.Stock) (domain.MenuItem, error) {
	return m.items.SetStock(ctx, id, stock)
}

// Remove убирает позицию из меню: площадка перестаёт видеть её в выдаче и деактивирует
// на витрине при ближайшей синхронизации каталога.
func (m *Menu) Remove(ctx context.Context, id uuid.UUID) error {
	return m.items.Remove(ctx, id)
}
