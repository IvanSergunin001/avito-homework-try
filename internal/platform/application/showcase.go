// Package application — сценарии площадки: то, что делает система, выраженное в терминах
// предметной области и не знающее ни о HTTP, ни о SQL.
package application

import (
	"context"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Showcase — витрина площадки: список заведений и карточка выбранного.
//
// Меню заведения витрина не отдаёт: это коллекция неограниченного размера со своей
// пагинацией, и приезжает она отдельным ресурсом.
type Showcase struct {
	restaurants domain.Restaurants
}

// NewShowcase собирает витрину над хранилищем заведений.
func NewShowcase(restaurants domain.Restaurants) *Showcase {
	return &Showcase{restaurants: restaurants}
}

// Restaurants отдаёт окно списка работающих заведений.
func (s *Showcase) Restaurants(ctx context.Context, page domain.Page) (domain.RestaurantPage, error) {
	return s.restaurants.ListWorking(ctx, page)
}

// Restaurant отдаёт карточку работающего заведения по идентификатору.
func (s *Showcase) Restaurant(ctx context.Context, id uuid.UUID) (domain.Restaurant, error) {
	return s.restaurants.FindWorking(ctx, id)
}
