// Package application — сценарии площадки: то, что делает система, выраженное в терминах
// предметной области и не знающее ни о HTTP, ни о SQL.
package application

import (
	"context"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Window — окно выдачи: сколько записей пропустить и сколько отдать. Границы окна
// проверяет тот, кто принимает запрос, — сценарию оно приходит уже приведённым к числам.
type Window struct {
	// Limit — сколько записей отдавать на страницу.
	Limit int
	// Offset — сколько записей пропустить от начала выдачи.
	Offset int
}

// RestaurantPage — окно списка заведений вместе с общим числом записей: без него клиент
// не знает, есть ли что листать дальше.
type RestaurantPage struct {
	// Restaurants — заведения запрошенного окна.
	Restaurants []domain.Restaurant
	// Total — сколько всего заведений удовлетворяет запросу, а не сколько попало в окно.
	Total int64
}

// Restaurants — доступ к заведениям площадки. Обе операции отдают только работающие
// заведения: «неработающее» — это не состояние, которое сценарий отфильтровывает после
// чтения, а часть самого запроса, иначе пагинация считала бы страницы по записям, которых
// покупатель не видит.
type Restaurants interface {
	// ListWorking отдаёт окно списка работающих заведений и общее их число.
	ListWorking(ctx context.Context, window Window) (RestaurantPage, error)
	// FindWorking отдаёт работающее заведение по идентификатору. Если заведения нет или
	// оно не работает — domain.ErrRestaurantNotFound.
	FindWorking(ctx context.Context, id uuid.UUID) (domain.Restaurant, error)
}

// Showcase — витрина площадки: список заведений и карточка выбранного.
//
// Меню заведения витрина не отдаёт: это коллекция неограниченного размера со своей
// пагинацией, и приезжает она отдельным ресурсом.
type Showcase struct {
	restaurants Restaurants
}

// NewShowcase собирает витрину над хранилищем заведений.
func NewShowcase(restaurants Restaurants) *Showcase {
	return &Showcase{restaurants: restaurants}
}

// Restaurants отдаёт окно списка работающих заведений.
func (s *Showcase) Restaurants(ctx context.Context, window Window) (RestaurantPage, error) {
	return s.restaurants.ListWorking(ctx, window)
}

// Restaurant отдаёт карточку работающего заведения по идентификатору.
func (s *Showcase) Restaurant(ctx context.Context, id uuid.UUID) (domain.Restaurant, error) {
	return s.restaurants.FindWorking(ctx, id)
}
