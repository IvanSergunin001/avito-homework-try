package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/partner_v1"
)

const (
	// partnerBaseURL — префикс партнёрского контракта в коробке. Сами пути берутся из
	// спецификации, здесь только то, что в ней вынесено в адрес сервера.
	partnerBaseURL = "/api/v1"

	// Границы страницы меню повторяют объявленные в партнёрской спецификации: страничная
	// выдача — обязанность заведения, а не рекомендация.
	menuDefaultPageSize = 100
	menuMaxPageSize     = 500
)

// partnerAPI обслуживает в коробке ту половину партнёрского контракта, которую обязано
// реализовать заведение. Коробка сдаёт тот же экзамен, что и заведение со своей системой:
// интерфейс сгенерирован по общему документу контракта.
//
// Встроенная сгенерированная заглушка отвечает `501` на операции, до которых очередь ещё
// не дошла: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе и перекрывает заглушку.
type partnerAPI struct {
	partner_v1.Unimplemented
}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router) {
	partner_v1.HandlerWithOptions(partnerAPI{}, partner_v1.ChiServerOptions{
		BaseURL:    partnerBaseURL,
		BaseRouter: r,
		// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
		// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ValidationFailed,
				"Некорректные параметры запроса", err.Error())
		},
	})
}

// ListMenu отдаёт страницу меню заведения.
//
// Хранилище коробки появляется на своём этапе, поэтому меню пока пусто. Пустая страница —
// не заглушка: это ровно тот ответ, который контракт обещает заведению, ещё не заведшему
// ни одной позиции.
func (partnerAPI) ListMenu(w http.ResponseWriter, r *http.Request, params partner_v1.ListMenuParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, menuDefaultPageSize, menuMaxPageSize)
	if err != nil {
		writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ValidationFailed,
			"Некорректные параметры запроса", err.Error())

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, partner_v1.MenuPage{
		Items: []partner_v1.MenuItem{},
		Page: partner_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
		},
	})
}

// writePartnerProblem отвечает отказом в схеме партнёрского контракта: тип сгенерирован
// по нему же, поэтому тело ответа не может разойтись с тем, что обещано заведению.
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
