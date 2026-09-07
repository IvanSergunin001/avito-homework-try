package application

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// Role — кем площадка признаёт того, кто пришёл с токеном.
type Role string

const (
	// RoleAdmin — сотрудник площадки: администратор, подключающий заведения, и саппорт,
	// разбирающий обращения. Роль одна на обоих: и тот и другой опознаётся одним админским
	// токеном из конфигурации, а разделять их правами имело бы смысл, только если бы
	// токены выдавались людям поимённо.
	RoleAdmin Role = "admin"
	// RolePartner — заведение, опознанное по хешу его партнёрского ключа.
	RolePartner Role = "partner"
)

// Caller — кто выполняет запрос к закрытой поверхности.
type Caller struct {
	// Role — кем площадка его признала.
	Role Role
	// Partner — заведение, которому выдан ключ; заполнено только у роли RolePartner.
	// Сотрудник площадки заведением не является, и подставить себе чужое он не может:
	// заведение берётся из хранилища по хешу ключа, а не из запроса.
	Partner domain.Partner
}

// ErrUnknownToken — токен не опознан: его нет ни среди партнёрских ключей, ни среди
// токенов из конфигурации. Причина не уточняется намеренно: перебирающему токены не стоит
// подсказывать, какой из двух видов он почти угадал.
var ErrUnknownToken = errors.New("unknown token")

// Gatekeeper опознаёт того, кто пришёл к закрытой поверхности площадки.
//
// Это не аутентификация человека — её задание прямо запрещает, — а минимальная защита
// интеграционных и админских операций: подмена адреса заведения означала бы увод чужих
// заказов, поэтому дать выполнять её кому угодно нельзя.
type Gatekeeper struct {
	partners   Partners
	adminToken string
}

// NewGatekeeper собирает опознание над хранилищем заведений и админским токеном из
// конфигурации площадки.
func NewGatekeeper(partners Partners, adminToken string) *Gatekeeper {
	return &Gatekeeper{partners: partners, adminToken: adminToken}
}

// Identify опознаёт предъявленный токен.
//
// Сначала сверяется админский токен, потом ищется заведение: сравнение с одной строкой
// не ходит в базу, и незнакомый токен не превращается в запрос к хранилищу на каждый
// перебор. Сравнение — постоянное по времени: обычное сравнение строк заканчивается на
// первом различии, и по времени ответа токен подбирается знак за знаком.
func (g *Gatekeeper) Identify(ctx context.Context, token string) (Caller, error) {
	// Пустой токен в конфигурации означает, что админских прав нет ни у кого:
	// иначе служба, поднятая без токена, принимала бы пустой токен как настоящий.
	if g.adminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(g.adminToken)) == 1 {
		return Caller{Role: RoleAdmin}, nil
	}

	// Заведение ищется по хешу: открытого ключа у площадки нет, и сравнивать ей не с чем.
	// Отключённое заведение по своему ключу не находится — отключили его затем, чтобы оно
	// перестало работать с площадкой, а не только исчезло с витрины.
	partner, err := g.partners.FindByKeyHash(ctx, domain.HashPartnerKey(token))

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return Caller{}, ErrUnknownToken
	case err != nil:
		return Caller{}, err
	}

	return Caller{Role: RolePartner, Partner: partner}, nil
}
