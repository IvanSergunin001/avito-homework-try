package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
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

			partner, err := g.gate.IdentifyPartner(r.Context(), key)

			switch {
			case errors.Is(err, application.ErrUnknownKey):
				w.Header().Set(challengeHeader, invalidKeyChallenge)
				// Что именно не так с ключом, наружу не уходит: подсказка «ключ был
				// партнёрским, но заведение отключено» помогает перебирающему, а
				// владельцу ключа не говорит ничего, чего он не узнает у площадки.
				refusePartnerAccess(w, r, "ключ не опознан")

				return
			case err != nil:
				partnerProblems.Failure(g.log, w, r, err)

				return
			}

			// Опознанное заведение кладётся в контекст запроса: обработчику оно нужно
			// целиком (`GET /integration` отвечает реквизитами звонящего), а искать его
			// второй раз по тому же ключу значило бы сходить в базу дважды за один
			// запрос и допустить, что два похода нашли разное. Из самого запроса заведение
			// не берётся никогда: иначе предъявитель одного ключа говорил бы от имени соседа.
			next.ServeHTTP(w, r.WithContext(withPartner(r.Context(), partner)))
		})
	}
}

// partnerContextKey — ключ, под которым опознанное заведение лежит в контексте запроса.
// Свой неэкспортируемый тип, а не строка: чужой пакет не может ни прочитать значение
// случайно, ни затереть его своим.
type partnerContextKey struct{}

// withPartner кладёт опознанное заведение в контекст запроса.
func withPartner(ctx context.Context, partner domain.Partner) context.Context {
	return context.WithValue(ctx, partnerContextKey{}, partner)
}

// partnerFromContext достаёт заведение, опознанное проверкой ключа.
//
// Второе значение — не «вдруг не найдётся»: до обработчиков партнёрского API запрос
// доходит только через requirePartner, и его отсутствие означало бы, что маршрут
// смонтировали мимо проверки ключа. Обработчик отвечает на это отказом по вине площадки,
// а не пустым заведением, — молча отдать чужому реквизиты хуже, чем ответить `500`.
func partnerFromContext(ctx context.Context) (domain.Partner, bool) {
	partner, ok := ctx.Value(partnerContextKey{}).(domain.Partner)

	return partner, ok
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
