package application

import (
	"context"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// ProductQuery — что спрашивают у каталога: подстрока названия, окно выдачи и, если поиск
// сужен до одного заведения, его идентификатор.
type ProductQuery struct {
	// Restaurant — заведение, которым ограничена выдача. Пустой идентификатор означает
	// «по всем заведениям»: искать блюдо, а не заведение, — самостоятельный сценарий, и
	// идентификатора у него нет. Настоящий идентификатор пустым не бывает.
	Restaurant uuid.UUID
	// NameContains — подстрока названия. Пустая означает «без отбора по названию»:
	// отсутствие параметра и пустая строка для клиента одно и то же.
	NameContains string
	// Page — окно выдачи.
	Page domain.Page
}

// ProductPage — окно выдачи товаров вместе с общим числом подошедших записей.
type ProductPage struct {
	// Products — товары запрошенного окна.
	Products []domain.Product
	// Total — сколько всего товаров удовлетворяет запросу, а не сколько попало в окно.
	Total int64
}

// Products — доступ к товарам площадки. Обе операции видят только товары работающих
// заведений: закрылось заведение — исчезли и его блюда, иначе покупатель нашёл бы
// поиском то, что заказать нельзя.
type Products interface {
	// Search отдаёт окно товаров, подошедших под запрос, и общее их число.
	Search(ctx context.Context, query ProductQuery) (ProductPage, error)
	// Find отдаёт товар работающего заведения по идентификатору. Если товара нет или его
	// заведение не работает — domain.ErrProductNotFound.
	Find(ctx context.Context, id uuid.UUID) (domain.Product, error)
}

// Catalog — каталог товаров площадки: меню отдельного заведения, поиск блюда по всем
// заведениям сразу и карточка товара.
//
// Каталог живёт отдельно от витрины заведений (Showcase) не по слоям, а по размеру:
// меню — коллекция неограниченного размера со своей пагинацией, и приезжает она своим
// ресурсом, а не внутри карточки заведения.
type Catalog struct {
	restaurants domain.Restaurants
	products    Products
}

// NewCatalog собирает каталог над хранилищами заведений и товаров.
func NewCatalog(restaurants domain.Restaurants, products Products) *Catalog {
	return &Catalog{restaurants: restaurants, products: products}
}

// Menu отдаёт страницу меню заведения; непустая подстрока сужает её до товаров, чьё
// название её содержит.
//
// Работает ли заведение, проверяется до чтения меню: пустое меню закрытого заведения
// неотличимо от пустого меню работающего, а покупателю нужно понять, что заведение
// больше не работает, а не что у него кончились блюда.
func (c *Catalog) Menu(
	ctx context.Context, restaurant uuid.UUID, nameContains string, page domain.Page,
) (ProductPage, error) {
	if _, err := c.restaurants.FindWorking(ctx, restaurant); err != nil {
		return ProductPage{}, err
	}

	return c.products.Search(ctx, ProductQuery{
		Restaurant:   restaurant,
		NameContains: nameContains,
		Page:         page,
	})
}

// Search ищет товар по названию по всем работающим заведениям сразу.
func (c *Catalog) Search(ctx context.Context, nameContains string, page domain.Page) (ProductPage, error) {
	return c.products.Search(ctx, ProductQuery{NameContains: nameContains, Page: page})
}

// Product отдаёт карточку товара по идентификатору.
func (c *Catalog) Product(ctx context.Context, id uuid.UUID) (domain.Product, error) {
	return c.products.Find(ctx, id)
}
