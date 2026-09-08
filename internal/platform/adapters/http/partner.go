package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
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

	catalog  *application.CatalogSync
	progress *application.Progress
	log      *slog.Logger
}

// mountPartnerAPI монтирует партнёрскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(
	r chi.Router, access guard, catalog *application.CatalogSync,
	progress *application.Progress, log *slog.Logger,
) {
	api := partnerAPI{catalog: catalog, progress: progress, log: log}

	r.Group(func(r chi.Router) {
		// Проверка токена стоит перед всем остальным, включая разбор тела: посторонний не
		// должен даже узнать, что запрос неверно составлен, — ответ «у вас опечатка в
		// поле» рассказывает о поверхности больше, чем ответ «вас сюда не пускают».
		r.Use(access.require(application.RolePartner, partnerRefusals))
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, api.refuseOversizedBody))

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

// NotifyOrderStatus принимает уведомление заведения о смене статуса заказа.
//
// Так и только так статус заказа двигается после того, как заведение за него взялось:
// площадка ничего не опрашивает — она узнаёт новости от той стороны, которая их знает.
//
// Ответ `200` заведение получает и тогда, когда его уведомление ничего не изменило:
// повтор, опоздавшее уведомление и уведомление о заказе, который уже прожил свою жизнь,
// приняты — повторять их незачем. В теле ответа действующий статус заказа, и по нему
// видно, применилось уведомление или опоздало.
//
// Правила переходов здесь не проверяются вовсе: можно ли заказу отсюда туда и вправе ли
// этого требовать заведение, решает его агрегат — тот же самый, который отвечает на этот
// вопрос покупателю и саппорту.
func (a partnerAPI) NotifyOrderStatus(w http.ResponseWriter, r *http.Request, orderID partner_v1.OrderId) {
	caller, ok := callerFrom(r.Context())
	if !ok {
		writeInternalFailure(a.log, w, r, errors.New("партнёрская операция выполнена без опознания"))

		return
	}

	var body partner_v1.StatusNotification
	if err := decodeBody(r, &body, "status", "occurred_at"); err != nil {
		a.refuseNotification(w, r, domain.InvalidOrderError{Reason: err.Error()})

		return
	}
	// Перечень статусов уведомления закрыт контрактом, и проверяет его порождённое по
	// контракту перечисление, а не площадка своим списком: `pending` и `under_review`
	// ставит она сама, и уведомление о них — не опоздание, а нарушение контракта.
	if !body.Status.Valid() {
		a.refuseNotification(w, r, domain.InvalidOrderError{
			Reason: fmt.Sprintf("status: %q — не то состояние, о котором уведомляет заведение", body.Status),
		})

		return
	}

	notice := application.StatusNotice{
		// Словарь состояний у площадки и у заведения общий — тот, что объявлен
		// контрактом, — поэтому перевода между ними нет и разойтись им негде.
		Status:     domain.OrderStatus(body.Status),
		OccurredAt: body.OccurredAt,
	}
	if body.Reason != nil {
		notice.Reason = *body.Reason
	}

	order, err := a.progress.Notify(r.Context(), caller.Partner, orderID, notice)
	if err != nil {
		a.refuseNotification(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, partner_v1.OrderStatusState{
		OrderId: order.ID,
		Status:  partner_v1.OrderStatusStateStatus(order.Status),
	})
}

// refuseNotification отвечает на ошибку приёма уведомления.
//
// Отказ матрицы переходов доезжает заведению как непринятый запрос (`400`), а не как
// доменный конфликт: уведомление о состоянии, в которое заказ отсюда не переходит, —
// это расхождение заведения с контрактом, а не спор о судьбе заказа. Из ответа видно,
// что именно не сошлось: в `detail` — переход, которого потребовали.
//
// Заказ чужого заведения неотличим от несуществующего: по разным ответам перебор
// идентификаторов рассказал бы заведению, какие заказы есть у соседей.
func (a partnerAPI) refuseNotification(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid    domain.InvalidOrderError
		transition domain.OrderTransitionError
	)

	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		writePartnerProblem(w, r, http.StatusNotFound, partner_v1.ProblemCodeOrderNotFound,
			"Заказ не найден", "заказа с таким идентификатором у вас нет")
	case errors.As(err, &transition):
		a.log.WarnContext(r.Context(), "restaurant notified about an impossible transition",
			slog.Any("error", transition))
		writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ProblemCodeValidationFailed,
			"Недопустимый переход", transition.Error())
	case errors.As(err, &invalid):
		writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ProblemCodeValidationFailed,
			"Некорректное уведомление", invalid.Reason)
	default:
		writeInternalFailure(a.log, w, r, err)
	}
}

// refuseOversizedBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне контракта нет.
func (a partnerAPI) refuseOversizedBody(w http.ResponseWriter, r *http.Request) {
	writePartnerProblem(w, r, http.StatusRequestEntityTooLarge, partner_v1.ProblemCodeValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes))
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
