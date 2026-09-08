package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/partner_v1"
)

const (
	// PartnerAPIPrefix — префикс партнёрского контракта в коробке. Сегмент говорит, чья
	// это сторона: обязательства заведения живут под `restaurant`, ручки площадки —
	// под `partner` на самой площадке.
	PartnerAPIPrefix = "/api/kitchen/v1/restaurant"

	// MenuPageDefault — размер страницы меню, когда площадка не просила своего;
	// значение сверяется со спецификацией тестом.
	MenuPageDefault = 100
	// MenuPageMax — потолок страницы меню, объявленный партнёрским контрактом.
	MenuPageMax = 500
)

// partnerAPI обслуживает в коробке ту половину партнёрского контракта, которую обязано
// реализовать заведение. Коробка сдаёт тот же экзамен, что и заведение со своей системой:
// интерфейс сгенерирован по общему документу контракта.
type partnerAPI struct{}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router) {
	handler := partner_v1.NewStrictHandlerWithOptions(partnerAPI{}, nil, partner_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writePartnerRequestProblem,
		ResponseErrorHandlerFunc: writePartnerResponseProblem,
	})

	partner_v1.HandlerWithOptions(handler, partner_v1.ChiServerOptions{
		BaseURL:    PartnerAPIPrefix,
		BaseRouter: r,
		// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
		// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
		ErrorHandlerFunc: writePartnerRequestProblem,
	})
}

// ListMenu отдаёт страницу меню заведения.
//
// Хранилище коробки появляется на своём этапе, поэтому меню пока пусто. Пустая страница —
// не заглушка: это ровно тот ответ, который контракт обещает заведению, ещё не заведшему
// ни одной позиции.
func (partnerAPI) ListMenu(
	_ context.Context, request partner_v1.ListMenuRequestObject,
) (partner_v1.ListMenuResponseObject, error) {
	paging, err := httpx.ResolvePaging(
		request.Params.Limit, request.Params.Offset, MenuPageDefault, MenuPageMax)
	if err != nil {
		return partner_v1.ListMenu400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: partner_v1.
				BadRequestApplicationProblemPlusJSONResponse(partnerValidationProblem(err)),
		}, nil
	}

	return partner_v1.ListMenu200JSONResponse{
		Items: []partner_v1.MenuItem{},
		Page: partner_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
		},
	}, nil
}

// Приём и отмена заказа описаны контрактом, их обработчики подставляются своими этапами.

func (partnerAPI) AcceptOrder(
	context.Context, partner_v1.AcceptOrderRequestObject,
) (partner_v1.AcceptOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (partnerAPI) CancelAcceptedOrder(
	context.Context, partner_v1.CancelAcceptedOrderRequestObject,
) (partner_v1.CancelAcceptedOrderResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// partnerValidationProblem собирает отказ по неверному запросу. Тело — общая основа
// `problem+json`: своих полей контракту на `400` добавить нечего, и тип сгенерирован по
// спецификации, поэтому ответ не может разойтись с тем, что обещано заведению.
func partnerValidationProblem(err error) partner_v1.ProblemBase {
	detail := err.Error()

	return partner_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  "Некорректные параметры запроса",
		Status: http.StatusBadRequest,
		Detail: &detail,
		Code:   partner_v1.ValidationFailed,
	}
}

// writePartnerRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// нечисловой `limit`, неразбираемое тело.
func writePartnerRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	problem.Write(r.Context(), w, http.StatusBadRequest, partnerValidationProblem(err))
}

// writePartnerResponseProblem отвечает, когда обработчик вернул ошибку. Операция,
// описанная контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
func writePartnerResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	status, title, code := http.StatusInternalServerError, "Внутренняя ошибка коробки",
		partner_v1.InternalError
	if errors.Is(err, httpx.ErrNotImplemented) {
		status, title, code = http.StatusNotImplemented, "Операция ещё не реализована",
			partner_v1.NotImplemented
	}

	detail := err.Error()
	problem.Write(r.Context(), w, status, partner_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
