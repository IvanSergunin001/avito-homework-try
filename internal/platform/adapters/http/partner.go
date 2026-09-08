package http

import (
	"errors"
	"log/slog"
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

	catalog *application.CatalogSync
	log     *slog.Logger
}

// mountPartnerAPI монтирует партнёрскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, access guard, catalog *application.CatalogSync, log *slog.Logger) {
	api := partnerAPI{catalog: catalog, log: log}

	r.Group(func(r chi.Router) {
		r.Use(access.require(application.RolePartner, partnerRefusals))

		partner_v1.HandlerWithOptions(api, partner_v1.ChiServerOptions{
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

// RefreshCatalog забирает меню заведения внеочередно — не дожидаясь очередного цикла
// синхронизации.
//
// Заведение зовёт эту ручку затем, чтобы изменение цены или стоп-листа доехало до витрины
// сразу, поэтому меню забирается прямо здесь, а не откладывается в очередь: отложенное
// «немедленное обновление» отличалось бы от планового только названием.
//
// Ответ — `202` в любом случае, и это не небрежность, а контракт: заведение просит
// площадку сходить за меню, а не сообщить, что она о нём думает. Неудачная выгрузка
// каталога не касается вовсе — заведение остаётся на витрине с прежним меню, — и
// рассказать о ней есть кому: она уходит в лог площадки.
func (a partnerAPI) RefreshCatalog(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFrom(r.Context())
	if !ok {
		writeInternalFailure(a.log, w, r, errors.New("партнёрская операция выполнена без опознания"))

		return
	}

	if err := a.catalog.Refresh(r.Context(), caller.Partner); err != nil {
		a.log.WarnContext(r.Context(), "on-demand catalog sync failed",
			slog.String("restaurant_id", caller.Partner.ID.String()), slog.Any("error", err))
	}

	w.WriteHeader(http.StatusAccepted)
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
