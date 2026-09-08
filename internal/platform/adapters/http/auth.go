package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
)

const (
	// authorizationHeader и bearerScheme — стандартный заголовок авторизации и схема,
	// которой закрыты партнёрская и админская поверхности.
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
	// invalidTokenChallenge — вызов на предъявленный, но неопознанный токен: RFC 6750
	// просит отличать «токен не годится» от «токена не было», чтобы клиент не повторял
	// запрос с тем же токеном бесконечно.
	invalidTokenChallenge = bearerScheme + ` error="invalid_token"`
)

// accessRefusals — как поверхность отказывает в доступе. Тело отказа описано её
// спецификацией и порождено по ней, поэтому общая проверка токена его не выбирает: она
// решает, пускать или нет, а отвечает поверхность своей схемой.
type accessRefusals struct {
	// unauthorized — токена нет или он неизвестен.
	unauthorized func(w http.ResponseWriter, r *http.Request, detail string)
	// forbidden — предъявителя опознали, но эта операция не для него.
	forbidden func(w http.ResponseWriter, r *http.Request, detail string)
}

// guard закрывает поверхности площадки от посторонних.
type guard struct {
	gate *application.Gatekeeper
	log  *slog.Logger
}

// require пропускает дальше только того, кого площадка опознала ролью want.
//
// Разделение на `401` и `403` не косметическое: `401` означает «предъяви другой токен» и
// сопровождается названием схемы, `403` — «этот токен не откроет эту операцию, другого
// у тебя нет». Партнёр, дёрнувший админскую ручку, получает именно `403`: он опознан, и
// притворяться, что операции не существует, незачем — он всё равно знает контракт.
func (g guard) require(want application.Role, refuse accessRefusals) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r.Header.Get(authorizationHeader))
			if !ok {
				w.Header().Set(challengeHeader, bearerScheme)
				refuse.unauthorized(w, r, "нужен токен в заголовке Authorization по схеме Bearer")

				return
			}

			caller, err := g.gate.Identify(r.Context(), token)

			switch {
			case errors.Is(err, application.ErrUnknownToken):
				w.Header().Set(challengeHeader, invalidTokenChallenge)
				// Что именно не так с токеном, наружу не уходит: подсказка «ключ был
				// партнёрским, но заведение отключено» помогает перебирающему, а
				// владельцу ключа не говорит ничего, чего он не узнает у площадки.
				refuse.unauthorized(w, r, "токен не опознан")

				return
			case err != nil:
				writeInternalFailure(g.log, w, r, err)

				return
			}

			if caller.Role != want {
				refuse.forbidden(w, r, "эта операция не для предъявленного токена")

				return
			}

			// Опознанный уезжает дальше в контексте запроса: партнёрская операция
			// выполняется от имени конкретного заведения, а взять его обработчику больше
			// неоткуда — из запроса заведение не берётся никогда, иначе предъявитель
			// одного ключа говорил бы от имени соседа.
			next.ServeHTTP(w, r.WithContext(withCaller(r.Context(), caller)))
		})
	}
}

// callerKey — ключ опознанного в контексте запроса. Тип собственный и неэкспортируемый:
// с ним чужой пакет не подменит значение, положив в контекст свою строку.
type callerKey struct{}

// withCaller кладёт опознанного в контекст запроса.
func withCaller(ctx context.Context, caller application.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// callerFrom достаёт опознанного из контекста запроса.
//
// Отсутствие означает, что обработчик закрытой поверхности выполнился в обход проверки
// токена, — то есть ошибку сборки роутера, а не запроса: сообщать о ней клиенту нечего,
// и вызывающая сторона отвечает на неё как на отказ по вине площадки.
func callerFrom(ctx context.Context) (application.Caller, bool) {
	caller, ok := ctx.Value(callerKey{}).(application.Caller)

	return caller, ok
}

// bearerToken достаёт токен из значения заголовка авторизации.
//
// Название схемы сверяется без учёта регистра: RFC 7235 объявляет его нечувствительным к
// регистру, и клиент, приславший `bearer`, прав — отказывать ему значило бы спорить со
// стандартом там, где спор ничего не защищает.
func bearerToken(header string) (string, bool) {
	scheme, credentials, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", false
	}

	token := strings.TrimSpace(credentials)
	if token == "" {
		return "", false
	}

	return token, true
}

// writeInternalFailure отвечает на отказ по вине площадки. Причина уходит в лог, а не
// клиенту: наружу она рассказала бы об устройстве службы, а выбрать по ней ветку
// поведения клиент всё равно не может — ему остаётся повторить запрос позже.
func writeInternalFailure(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	log.ErrorContext(r.Context(), "platform request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, internalProblem{
		Type:   problem.DefaultType,
		Title:  "Внутренняя ошибка",
		Status: http.StatusInternalServerError,
	})
}
