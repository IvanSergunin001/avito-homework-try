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
	// Add подключает заведение с хешем выданного ему ключа и возвращает его таким, каким
	// оно легло в хранилище: с метками времени, которые ставит база.
	Add(ctx context.Context, partner domain.Partner, keyHash string) (domain.Partner, error)
	// Find находит заведение по идентификатору, подключённое или отключённое. Если его
	// нет — domain.ErrRestaurantNotFound.
	Find(ctx context.Context, id uuid.UUID) (domain.Partner, error)
	// Replace заменяет реквизиты подключённого заведения и возвращает их новыми. Если его
	// нет — domain.ErrRestaurantNotFound.
	Replace(ctx context.Context, partner domain.Partner) (domain.Partner, error)
	// FindByKeyHash находит подключённое заведение по хешу его ключа. Если такого нет —
	// domain.ErrRestaurantNotFound.
	FindByKeyHash(ctx context.Context, keyHash string) (domain.Partner, error)
}

// Requisites — правка реквизитов заведения: заполнены только те поля, которые
// администратор прислал, остальные остаются как были.
//
// Указатели, а не значения: отличить «поле не прислали» от «поле обнулили» иначе нельзя,
// а разница здесь дорогая — правка без отметки подключения тихо отключила бы заведение,
// и узнал бы об этом не администратор, а покупатель, не нашедший его на витрине.
type Requisites struct {
	// Name — новое название заведения.
	Name *string
	// BaseURL — новый адрес интеграции.
	BaseURL *string
	// IsActive — подключено ли заведение.
	IsActive *bool
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

	added, err := o.partners.Add(ctx, partner, domain.HashPartnerKey(key))
	if err != nil {
		return Registration{}, err
	}

	return Registration{Partner: added, Key: key}, nil
}

// Change правит реквизиты заведения: название, адрес интеграции и подключено ли оно.
// Меняется только то, что прислали, — чтобы отключить заведение, знать его адрес не нужно.
// Ключ не трогается ни при какой правке: переезд заведения не повод менять его секрет.
//
// Пустая правка — отказ, а не тихое согласие: см. первую проверку.
//
// Правка читает заведение и пишет его обратно, а не собирает `UPDATE` из присланных
// полей: правило «у заведения есть название и адрес, по которому до него можно дойти»
// проверяется целиком и после правки, а не по кусочкам до неё. Два запроса вместо одного
// здесь не гонка за состояние: одновременная правка одного заведения двумя
// администраторами — это не то, что случается с онбордингом, а последняя всё равно
// побеждает.
func (o *Onboarding) Change(
	ctx context.Context, id uuid.UUID, requisites Requisites,
) (domain.Partner, error) {
	// Правка без единого поля — не «ничего не меняем», а запрос, в котором забыли сказать,
	// что менять: администратор его составил, но не дописал. Принять его значило бы
	// переписать отметку `updated_at` и соврать о том, когда реквизиты трогали последний
	// раз. Спецификация обещает на такое тело отказ (`minProperties: 1`), и проверяется он
	// до похода в хранилище: неполный запрос неверен сам по себе, независимо от того,
	// существует заведение или нет.
	if requisites.Name == nil && requisites.BaseURL == nil && requisites.IsActive == nil {
		return domain.Partner{}, domain.InvalidPartnerError{
			Reason: "правка пустая: пришлите хотя бы одно поле — название, адрес интеграции или отметку подключения",
		}
	}

	current, err := o.partners.Find(ctx, id)
	if err != nil {
		return domain.Partner{}, err
	}

	name, baseURL, isActive := current.Name, current.BaseURL, current.IsActive

	if requisites.Name != nil {
		name = *requisites.Name
	}
	if requisites.BaseURL != nil {
		baseURL = *requisites.BaseURL
	}
	if requisites.IsActive != nil {
		isActive = *requisites.IsActive
	}

	partner, err := domain.NewPartner(id, name, baseURL, isActive)
	if err != nil {
		return domain.Partner{}, err
	}

	return o.partners.Replace(ctx, partner)
}
