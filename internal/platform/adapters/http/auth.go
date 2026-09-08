package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IvanSergunin001/Test157/internal/platform/application"
)

const (
	// authorizationHeader и bearerScheme — стандартный заголовок авторизации и схема,
	// которой закрыто партнёрское API площадки.
	//
	// Стандартный заголовок выбран вместо собственного `X-`-заголовка не из любви к
	// стандартам: прокси, логгеры и системы трассировки маскируют `Authorization` по
	// умолчанию, а произвольный заголовок утекает в логи открытым текстом — и партнёрский
	// ключ оказывается в первом же дампе запроса.
	authorizationHeader = "Authorization"
	bearerScheme        = "Bearer"

	// challengeHeader — заголовок, которым ответ `401` называет схему аутентификации:
	// без него клиент знает, что его не пустили, но не знает, чем стучаться (RFC 7235).
	challengeHeader = "WWW-Authenticate"
	// invalidKeyChallenge — вызов на предъявленный, но неопознанный ключ: RFC 6750
	// просит отличать «ключ не годится» от «ключа не было», чтобы клиент не повторял
	// запрос с тем же ключом бесконечно.
	invalidKeyChallenge = bearerScheme + ` error="invalid_token"`
)

// guard закрывает партнёрское API площадки от посторонних.
//
// Закрыто им одно API из трёх: клиентское открыто по природе, админское — по решению,
// записанному в его спецификации и в README. Ключ есть только у заведения, потому что
// только за ним стоит другая организация, двигающая чужие заказы.
type guard struct {
	gate *application.Gatekeeper
	log  *slog.Logger
}

// requirePartner пропускает дальше только того, чей ключ площадка опознала как ключ
// подключённого заведения.
//
// Отказ — всегда `401` с названием схемы: других предъявителей у площадки не осталось,
// и различать «ключ не тот» и «ключ не для этой операции» ей нечем. Тело отказа пишется
// схемой партнёрского контракта: до сгенерированных типов ответа посторонний запрос не
// доходит, но отвечать ему площадка обязана тем же, что обещала контрактом.
func (g guard) requirePartner() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := bearerToken(r.Header.Get(authorizationHeader))
			if !ok {
				w.Header().Set(challengeHeader, bearerScheme)
				refusePartnerAccess(w, r, "нужен партнёрский ключ в заголовке Authorization по схеме Bearer")

				return
			}

			// Кто именно позвонил, здесь не запоминается: операциям партнёрского API
			// на этом этапе достаточно того, что звонящий опознан, а тем, которым
			// понадобится само заведение, оно приедет своим этапом.
			_, err := g.gate.IdentifyPartner(r.Context(), key)

			switch {
			case errors.Is(err, application.ErrUnknownKey):
				w.Header().Set(challengeHeader, invalidKeyChallenge)
				// Что именно не так с ключом, наружу не уходит: подсказка «ключ был
				// партнёрским, но заведение отключено» помогает перебирающему, а
				// владельцу ключа не говорит ничего, чего он не узнает у площадки.
				refusePartnerAccess(w, r, "ключ не опознан")

				return
			case err != nil:
				writePartnerFailure(g.log, w, r, err)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken достаёт ключ из значения заголовка авторизации.
//
// Название схемы сверяется без учёта регистра: RFC 7235 объявляет его нечувствительным к
// регистру, и клиент, приславший `bearer`, прав — отказывать ему значило бы спорить со
// стандартом там, где спор ничего не защищает.
func bearerToken(header string) (string, bool) {
	scheme, credentials, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", false
	}

	key := strings.TrimSpace(credentials)
	if key == "" {
		return "", false
	}

	return key, true
}
