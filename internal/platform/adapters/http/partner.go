package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
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
	catalog *application.CatalogSync
	log     *slog.Logger
}

// mountPartnerAPI монтирует партнёрское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountPartnerAPI(r chi.Router, access guard, catalog *application.CatalogSync, log *slog.Logger) {
	api := partnerAPI{catalog: catalog, log: log}

	handler := partner_v1.NewStrictHandlerWithOptions(api, nil, partner_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  partnerProblems.BadRequest,
		ResponseErrorHandlerFunc: api.writePartnerFailure,
	})

	r.Group(func(r chi.Router) {
		// Ключ проверяется перед всем остальным, включая разбор тела: посторонний не
		// должен даже узнать, что запрос неверно составлен, — ответ «у вас опечатка в
		// поле» рассказывает об API больше, чем ответ «вас сюда не пускают».
		r.Use(access.requirePartner())
		// Предел на тело — после проверки ключа и перед сгенерированными маршрутами:
		// ключ проверяется первым (см. выше), а размер тела — раньше всего остального,
		// потому что разбор параметров и тела уже читает запрос. Потолок один на все три
		// API площадки; отказ отдаётся по схеме партнёрского контракта.
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, partnerProblems.Oversized))

		partner_v1.HandlerWithOptions(handler, partner_v1.ChiServerOptions{
			BaseURL:    PartnerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути. Контракт
			// обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: partnerProblems.BadRequest,
		})
	})
}

// GetIntegration показывает заведению, по каким адресам площадка будет к нему ходить.
//
// Реквизиты отдаются тому заведению, чьим ключом спросили: заведение опознано проверкой
// ключа, и брать идентификатор из запроса было бы приглашением посмотреть на чужой
// адрес. Ручка нужна затем, что склейка адреса интеграции с путями контракта живёт на
// площадке, а проверить её результат может только заведение — до неё первым сообщением о
// неверном адресе был неотвеченный запрос в момент первого заказа.
func (a partnerAPI) GetIntegration(
	ctx context.Context, _ partner_v1.GetIntegrationRequestObject,
) (partner_v1.GetIntegrationResponseObject, error) {
	partner, ok := partnerFromContext(ctx)
	if !ok {
		return nil, errors.New("партнёрское API вызвано в обход проверки ключа: заведение не опознано")
	}

	endpoints, err := partner.Endpoints()
	if err != nil {
		return nil, fmt.Errorf("собрать адреса операций контракта: %w", err)
	}

	items := make([]partner_v1.IntegrationEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		items = append(items, partner_v1.IntegrationEndpoint{
			Operation: endpoint.Operation,
			Method:    endpoint.Method,
			Url:       endpoint.URL,
		})
	}

	return partner_v1.GetIntegration200JSONResponse{
		BaseUrl:   partner.BaseURL,
		Endpoints: items,
	}, nil
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
func (a partnerAPI) RefreshCatalog(
	ctx context.Context, _ partner_v1.RefreshCatalogRequestObject,
) (partner_v1.RefreshCatalogResponseObject, error) {
	partner, ok := partnerFromContext(ctx)
	if !ok {
		// Это ошибка сборки роутера, а не запроса: партнёрская ручка смонтирована в обход
		// проверки ключа. Заведению она отдаётся как отказ по вине площадки.
		return nil, errors.New("партнёрская операция выполнена без опознания заведения")
	}

	if err := a.catalog.Refresh(ctx, partner); err != nil {
		a.log.WarnContext(ctx, "on-demand catalog sync failed",
			slog.String("restaurant_id", partner.ID.String()), slog.Any("error", err))
	}

	return partner_v1.RefreshCatalog202Response{}, nil
}

// Уведомление о смене статуса заказа описано контрактом, его обработчик подставляется
// своим этапом.
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
	partnerProblems.Write(w, r, http.StatusUnauthorized, partner_v1.ProblemCodeUnauthorized,
		"Требуется партнёрский ключ", detail)
}

// partnerProblemBody собирает тело отказа партнёрского API площадки — общую основу
// `problem+json`. Своих полей у этих отказов нет, а тип сгенерирован по контракту,
// поэтому ответ не может с ним разойтись.
func partnerProblemBody(
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

// partnerProblems — отказы партнёрского API площадки. Форма у них та же, что у остальных
// четырёх поверхностей проекта, поэтому она живёт одна на всех (problem.Writer), а здесь
// названо только то, чем это API от них отличается: сгенерированные типы, коды и тексты.
var partnerProblems = problem.Writer[partner_v1.ProblemBase, partner_v1.ProblemCode]{
	Body: partnerProblemBody,
	Codes: problem.Codes[partner_v1.ProblemCode]{
		ValidationFailed: partner_v1.ProblemCodeValidationFailed,
		NotImplemented:   partner_v1.ProblemCodeNotImplemented,
		InternalError:    partner_v1.ProblemCodeInternalError,
	},
	MaxRequestBytes: maxRequestBodyBytes,
	FailureTitle:    "Внутренняя ошибка площадки",
	Surface:         "partner",
}

// writePartnerFailure отвечает на отказ по вине площадки. Отвечает им и обработчик, и
// проверка ключа: заведению всё равно, на каком шаге площадка не справилась.
func (a partnerAPI) writePartnerFailure(w http.ResponseWriter, r *http.Request, err error) {
	partnerProblems.Failure(a.log, w, r, err)
}
