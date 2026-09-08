package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Partners — заведения площадки со стороны интеграции: подключение, правка реквизитов и
// опознание звонящего по ключу.
//
// Порт отдельный от Restaurants, хотя данные за ними лежат в одной таблице: витрине нужны
// работающие заведения без реквизитов, онбордингу — реквизиты любого заведения. Один порт
// на оба сценария означал бы, что чтение витрины умеет отдавать адрес коробки.
type Partners interface {
	// Add подключает заведение с хешем выданного ему ключа.
	Add(ctx context.Context, partner domain.Partner, keyHash string) error
	// Replace заменяет реквизиты подключённого заведения. Если его нет —
	// domain.ErrRestaurantNotFound.
	Replace(ctx context.Context, partner domain.Partner) error
	// FindByKeyHash находит подключённое заведение по хешу его ключа. Если такого нет —
	// domain.ErrRestaurantNotFound.
	FindByKeyHash(ctx context.Context, keyHash string) (domain.Partner, error)
	// ListActive перечисляет подключённые заведения — те, у которых площадка забирает
	// меню. Список целиком, без окна выдачи: его читает не человек, а обход
	// синхронизации, и «страница заведений, которым сегодня не повезло» смысла не имеет.
	ListActive(ctx context.Context) ([]domain.Partner, error)
}

// Registration — только что подключённое заведение вместе с выданным ключом.
//
// Ключ едет отдельным полем, а не внутри Partner: он живёт ровно один ответ. В хранилище
// уезжает его хеш, и следующий читатель заведения ключа уже не увидит.
type Registration struct {
	// Partner — подключённое заведение.
	Partner domain.Partner
	// Key — партнёрский ключ открытым текстом: единственный раз, когда площадка его знает.
	Key string
}

// Onboarding — подключение заведений к площадке: регистрация нового и правка реквизитов
// существующего.
//
// Переезд заведения на другой адрес или порт — это правка реквизитов, а не выпуск новой
// версии площадки: адрес живёт данными, поэтому смена инфраструктуры заведения не
// требует ни правок в коде, ни перезапуска.
type Onboarding struct {
	partners Partners
}

// NewOnboarding собирает онбординг над хранилищем заведений.
func NewOnboarding(partners Partners) *Onboarding {
	return &Onboarding{partners: partners}
}

// Register подключает заведение и выдаёт ему партнёрский ключ.
//
// Идентификатор выдаёт площадка: он уезжает в ссылки витрины и обязан быть стабильным,
// а придуманный руками рано или поздно повторится. uuid v7 монотонный — клиентский
// первичный ключ не разносит вставки по индексу.
//
// Подключённое заведение сразу работает: регистрируют его затем, чтобы принимать заказы,
// и отдельный шаг «включить» означал бы, что половина заведений забыта в невидимом
// состоянии. Выключается заведение правкой реквизитов.
func (o *Onboarding) Register(ctx context.Context, name, baseURL string) (Registration, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Registration{}, fmt.Errorf("выдать идентификатор заведения: %w", err)
	}

	partner, err := domain.NewPartner(id, name, baseURL, true)
	if err != nil {
		return Registration{}, err
	}

	key := domain.NewPartnerKey()
	if err := o.partners.Add(ctx, partner, domain.HashPartnerKey(key)); err != nil {
		return Registration{}, err
	}

	return Registration{Partner: partner, Key: key}, nil
}

// Change заменяет реквизиты заведения целиком: название, адрес интеграции и подключено ли
// оно. Ключ при этом не трогается — переезд заведения не повод менять его секрет.
func (o *Onboarding) Change(
	ctx context.Context, id uuid.UUID, name, baseURL string, isActive bool,
) (domain.Partner, error) {
	partner, err := domain.NewPartner(id, name, baseURL, isActive)
	if err != nil {
		return domain.Partner{}, err
	}

	if err := o.partners.Replace(ctx, partner); err != nil {
		return domain.Partner{}, err
	}

	return partner, nil
}
