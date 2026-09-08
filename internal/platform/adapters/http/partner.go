package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/partner_v1"
)

// PartnerAPIPrefix — префикс партнёрского API площадки: та половина контракта, которую
// площадка предоставляет заведению. Сегмент говорит, чья это сторона: обязательства
// заведения живут под `restaurant` в самой коробке. Сами пути берутся из спецификации,
// здесь только то, что в ней вынесено в адрес сервера.
const PartnerAPIPrefix = "/api/kitchen/v1/partner"

// partnerAPI обслуживает партнёрское API площадки.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
// Закрыто API уже сейчас: защита не приделывается к готовым ручкам задним числом — так и
// появляется ручка, которую забыли закрыть.
type partnerAPI struct {
	log *slog.Logger
}

// mountPartnerAPI монтирует партнёрское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountPartnerAPI(r chi.Router, access guard, log *slog.Logger) {
	api := partnerAPI{log: log}

	handler := partner_v1.NewStrictHandlerWithOptions(api, nil, partner_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writePartnerRequestProblem,
		ResponseErrorHandlerFunc: api.writePartnerResponseProblem,
	})

	r.Group(func(r chi.Router) {
		// Ключ проверяется перед всем остальным, включая разбор тела: посторонний не
		// должен даже узнать, что запрос неверно составлен, — ответ «у вас опечатка в
		// поле» рассказывает об API больше, чем ответ «вас сюда не пускают».
		r.Use(access.requirePartner())

		partner_v1.HandlerWithOptions(handler, partner_v1.ChiServerOptions{
			BaseURL:    PartnerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути. Контракт
			// обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: writePartnerRequestProblem,
		})
	})
}

// Обе операции описаны контрактом, их обработчики подставляются своими этапами:
// внеочередное обновление каталога — #10, уведомление о смене статуса заказа — своим.

func (partnerAPI) RefreshCatalog(
	context.Context, partner_v1.RefreshCatalogRequestObject,
) (partner_v1.RefreshCatalogResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

func (partnerAPI) NotifyOrderStatus(
	context.Context, partner_v1.NotifyOrderStatusRequestObject,
) (partner_v1.NotifyOrderStatusResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// refusePartnerAccess отказывает постороннему запросу в схеме партнёрского контракта.
//
// Отдаёт отказ проверка ключа, то есть middleware, а не обработчик: до сгенерированных
// типов ответа посторонний запрос не доходит, поэтому тело пишется напрямую — но той же
// схемой, что объявлена контрактом.
func refusePartnerAccess(w http.ResponseWriter, r *http.Request, detail string) {
	writePartnerProblem(w, r, http.StatusUnauthorized, partner_v1.ProblemCodeUnauthorized,
		"Требуется партнёрский ключ", detail)
}

// partnerProblem собирает отказ партнёрского API. Тело — общая основа `problem+json`:
// своих полей у этих отказов нет, а тип сгенерирован по контракту, поэтому ответ не может
// с ним разойтись.
func partnerProblem(
	status int, code partner_v1.ProblemCode, title, detail string,
) partner_v1.ProblemBase {
	return partner_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	}
}

// writePartnerProblem отвечает отказом в схеме партнёрского контракта.
func writePartnerProblem(
	w http.ResponseWriter, r *http.Request, status int, code partner_v1.ProblemCode, title, detail string,
) {
	problem.Write(r.Context(), w, status, partnerProblem(status, code, title, detail))
}

// writePartnerRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// неразбираемое тело.
func writePartnerRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ProblemCodeValidationFailed,
		"Некорректные параметры запроса", err.Error())
}

// writePartnerResponseProblem отвечает, когда обработчик вернул ошибку. Операция,
// описанная контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
func (a partnerAPI) writePartnerResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, httpx.ErrNotImplemented) {
		writePartnerProblem(w, r, http.StatusNotImplemented, partner_v1.ProblemCodeNotImplemented,
			"Операция ещё не реализована", err.Error())

		return
	}

	writePartnerFailure(a.log, w, r, err)
}

// writePartnerFailure отвечает на отказ по вине площадки. Отвечает им и обработчик, и
// проверка ключа: заведению всё равно, на каком шаге площадка не справилась.
//
// Причина уходит в лог, а не заведению: наружу она рассказала бы об устройстве службы,
// а выбрать по ней ветку поведения заведение всё равно не может — ему остаётся повторить
// запрос позже.
func writePartnerFailure(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	log.ErrorContext(r.Context(), "partner request failed", slog.Any("error", err))

	writePartnerProblem(w, r, http.StatusInternalServerError, partner_v1.ProblemCodeInternalError,
		"Внутренняя ошибка площадки", "повторите запрос позже")
}
