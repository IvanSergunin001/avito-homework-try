package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
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

	// maxRequestBodyBytes — потолок на тело входящего запроса, один на обе поверхности
	// коробки. Заказ от площадки — это список позиций корзины, позиция меню от оператора —
	// название, описание и два числа: мегабайта хватает с запасом и тому, и другому. Всё,
	// что больше, коробка не разбирает, а отклоняет: тело неограниченного размера читается
	// в память до того, как хоть одно правило успеет сказать, что запрос неверен.
	maxRequestBodyBytes = 1 * httpx.MB
)

// partnerAPI обслуживает в коробке ту половину партнёрского контракта, которую обязано
// реализовать заведение. Коробка сдаёт тот же экзамен, что и заведение со своей системой:
// интерфейс сгенерирован по общему документу контракта.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
type partnerAPI struct {
	menu *application.Menu
	log  *slog.Logger
}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, menu *application.Menu, log *slog.Logger) {
	api := partnerAPI{menu: menu, log: log}

	handler := partner_v1.NewStrictHandlerWithOptions(api, nil, partner_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writePartnerRequestProblem,
		ResponseErrorHandlerFunc: api.writePartnerResponseProblem,
	})

	// Лимит тела стоит перед сгенерированными маршрутами: размер тела проверяется раньше
	// всего остального, потому что разбор параметров и тела уже читает запрос. Отказ
	// отдаётся по схеме партнёрского контракта — по ней коробка и отвечает.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, refuseOversizedPartnerBody))

		partner_v1.HandlerWithOptions(handler, partner_v1.ChiServerOptions{
			BaseURL:    PartnerAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`. Контракт обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: writePartnerRequestProblem,
		})
	})
}

// ListMenu отдаёт страницу меню заведения по партнёрскому контракту.
//
// Наружу уезжает меньше, чем видит оператор: остаток и отметка стоп-листа — внутренняя
// кухня заведения, площадке достаточно ответа «заказать можно или нельзя». Позиция,
// которой нет, из выдачи не пропадает: покупатель искал бы блюдо, которое вчера видел,
// и не находил его без объяснения.
func (a partnerAPI) ListMenu(
	ctx context.Context, request partner_v1.ListMenuRequestObject,
) (partner_v1.ListMenuResponseObject, error) {
	paging, err := httpx.ResolvePaging(
		request.Params.Limit, request.Params.Offset, MenuPageDefault, MenuPageMax)
	if err != nil {
		return partner_v1.ListMenu400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: partner_v1.
				BadRequestApplicationProblemPlusJSONResponse(partnerValidationProblem(err)),
		}, nil
	}

	// Синхронизация каталога от неудачного ответа не страдает: выгрузка применяется
	// целиком, и незаконченная оставляет площадке прежний каталог.
	page, err := a.menu.Page(ctx, application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		return nil, err
	}

	items := make([]partner_v1.MenuItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, contractItem(item))
	}

	return partner_v1.ListMenu200JSONResponse{
		Items: items,
		Page: partner_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
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

// contractItem переводит позицию меню в схему партнёрского контракта.
//
// Цена уезжает тем же целым числом копеек, каким хранится: перевод в дробное число по
// дороге означал бы, что у коробки и у витрины разные деньги.
func contractItem(item domain.MenuItem) partner_v1.MenuItem {
	contract := partner_v1.MenuItem{
		ExternalId:  item.ExternalID(),
		Name:        item.Name,
		Price:       int64(item.Price),
		IsAvailable: item.IsAvailable(),
	}

	// Описания у позиции может не быть — тогда поля нет и в ответе: пустая строка
	// сообщила бы площадке, что описание есть и оно пустое.
	if item.Description != "" {
		contract.Description = &item.Description
	}

	return contract
}

// partnerProblem собирает отказ партнёрского контракта. Тело — общая основа
// `problem+json`: своих полей у этих отказов нет, а тип сгенерирован по спецификации,
// поэтому ответ не может разойтись с тем, что обещано заведению.
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

// partnerValidationProblem собирает отказ по неверному запросу.
func partnerValidationProblem(err error) partner_v1.ProblemBase {
	return partnerProblem(http.StatusBadRequest, partner_v1.ValidationFailed,
		"Некорректные параметры запроса", err.Error())
}

// writePartnerRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// нечисловой `limit`, неразбираемое тело.
//
// Тело, оборвавшееся на пределе, приезжает сюда же и отдаётся тем же отказом, что и
// объявленная длина сверх предела: причина у площадки одна.
func writePartnerRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		refuseOversizedPartnerBody(w, r)

		return
	}

	problem.Write(r.Context(), w, http.StatusBadRequest, partnerValidationProblem(err))
}

// refuseOversizedPartnerBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет. Заводить новый код ради размера тела незачем — ветка
// поведения у площадки та же самая, что и при любом непринятом запросе.
func refuseOversizedPartnerBody(w http.ResponseWriter, r *http.Request) {
	detail := fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes)

	problem.Write(r.Context(), w, http.StatusRequestEntityTooLarge, partner_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  "Слишком большое тело запроса",
		Status: http.StatusRequestEntityTooLarge,
		Detail: &detail,
		Code:   partner_v1.ValidationFailed,
	})
}

// writePartnerResponseProblem отвечает, когда обработчик вернул ошибку. Операция,
// описанная контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
//
// Причина отказа по вине коробки уходит в лог, а не площадке: наружу она рассказала бы об
// устройстве коробки, а выбрать по ней ветку поведения площадка всё равно не может.
func (a partnerAPI) writePartnerResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, httpx.ErrNotImplemented) {
		problem.Write(r.Context(), w, http.StatusNotImplemented, partnerProblem(
			http.StatusNotImplemented, partner_v1.NotImplemented,
			"Операция ещё не реализована", err.Error()))

		return
	}

	a.log.ErrorContext(r.Context(), "menu request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, partnerProblem(
		http.StatusInternalServerError, partner_v1.InternalError,
		"Внутренняя ошибка коробки", "повторите запрос позже"))
}
