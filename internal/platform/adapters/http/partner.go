package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
)

// partnerBaseURL — префикс партнёрской поверхности: та половина контракта, которую
// предоставляет площадка заведению. Сами пути берутся из спецификации, здесь только то,
// что в ней вынесено в адрес сервера.
const partnerBaseURL = "/api/partner/v1"

// partnerAPI обслуживает партнёрскую поверхность площадки.
//
// Встроенная сгенерированная заглушка отвечает `501` на операции, до которых очередь ещё
// не дошла: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе и перекрывает заглушку. Закрыта поверхность уже сейчас:
// защита не приделывается к готовым ручкам задним числом — так и появляется ручка,
// которую забыли закрыть.
type partnerAPI struct {
	partner_v1.Unimplemented
}

// mountPartnerAPI монтирует партнёрскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, access guard) {
	r.Group(func(r chi.Router) {
		r.Use(access.require(application.RolePartner, partnerRefusals))

		partner_v1.HandlerWithOptions(partnerAPI{}, partner_v1.ChiServerOptions{
			BaseURL:    partnerBaseURL,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути. Контракт
			// обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ProblemCodeValidationFailed,
					"Некорректные параметры запроса", err.Error())
			},
		})
	})
}

// partnerRefusals — отказы в доступе к партнёрской поверхности в её собственной схеме.
//
// Админский токен здесь получает `403`, а не пропуск: партнёрская операция выполняется
// от имени конкретного заведения, а токен сотрудника площадки не называет заведения —
// выполнить её «вообще» нельзя, и подставлять заведение из запроса значило бы позволить
// звонить от чужого имени.
var partnerRefusals = accessRefusals{
	unauthorized: func(w http.ResponseWriter, r *http.Request, detail string) {
		writePartnerProblem(w, r, http.StatusUnauthorized, partner_v1.ProblemCodeUnauthorized,
			"Требуется партнёрский ключ", detail)
	},
	forbidden: func(w http.ResponseWriter, r *http.Request, detail string) {
		writePartnerProblem(w, r, http.StatusForbidden, partner_v1.ProblemCodeForbidden,
			"Операция заведения", detail)
	},
}

// writePartnerProblem отвечает отказом в схеме партнёрского контракта: тип сгенерирован
// по нему же, поэтому тело ответа не может разойтись с контрактом.
func writePartnerProblem(
	w http.ResponseWriter, r *http.Request, status int, code partner_v1.ProblemCode, title, detail string,
) {
	problem.Write(r.Context(), w, status, partner_v1.Problem{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
