package application

import (
	"context"
	"errors"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// ErrUnknownKey — предъявленный ключ не принадлежит ни одному подключённому заведению.
// Чем именно он плох, ошибка не уточняет: перебирающему ключи не стоит подсказывать,
// насколько близко он подошёл.
var ErrUnknownKey = errors.New("unknown partner key")

// Gatekeeper опознаёт заведение, пришедшее к партнёрскому API площадки.
//
// Это не аутентификация человека — её задание прямо запрещает, — а минимальная защита
// интеграционных операций: за партнёрским ключом стоит другая организация, двигающая
// чужие заказы, и выполнять их от имени заведения не должен кто угодно. Админские
// операции площадки, наоборот, ничем не закрыты: сотрудник называет себя тем же
// заголовком `User-Id`, что и покупатель, — цена этого упрощения названа в README.
type Gatekeeper struct {
	partners Partners
}

// NewGatekeeper собирает опознание над хранилищем заведений.
func NewGatekeeper(partners Partners) *Gatekeeper {
	return &Gatekeeper{partners: partners}
}

// IdentifyPartner находит заведение по предъявленному ключу.
//
// Заведение ищется по хешу: открытого ключа у площадки нет, и сравнивать ей не с чем.
// Отключённое заведение по своему ключу не находится — отключили его затем, чтобы оно
// перестало работать с площадкой, а не только исчезло с витрины.
func (g *Gatekeeper) IdentifyPartner(ctx context.Context, key string) (domain.Partner, error) {
	partner, err := g.partners.FindByKeyHash(ctx, domain.HashPartnerKey(key))

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return domain.Partner{}, ErrUnknownKey
	case err != nil:
		return domain.Partner{}, err
	}

	return partner, nil
}
