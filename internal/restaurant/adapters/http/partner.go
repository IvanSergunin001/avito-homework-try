package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/partner_v1"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
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

	menu   *application.Menu
	orders *application.Orders
	log    *slog.Logger
}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, menu *application.Menu, orders *application.Orders, log *slog.Logger) {
	api := partnerAPI{menu: menu, orders: orders, log: log}

	// Лимит тела вешается на поверхность, а не на весь роутер: отказ отдаётся по схеме
	// партнёрского контракта, а у рабочего места оператора схема своя. Он стоит перед
	// сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, api.refuseOversizedBody))

		partner_v1.HandlerWithOptions(api, partner_v1.ChiServerOptions{
			BaseURL:    partnerBaseURL,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`, отсутствующий заголовок идемпотентности. Контракт обещает на них
			// `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				refuseUnprocessableRequest(w, r, http.StatusBadRequest,
					"Некорректные параметры запроса", err.Error())
			},
		})
	})
}

// ListMenu отдаёт страницу меню заведения — то самое меню, которое ведёт оператор.
//
// Наружу уезжает меньше, чем видит оператор: остаток и отметка стоп-листа — внутренняя
// кухня заведения, площадке достаточно ответа «заказать можно или нельзя». Позиция,
// которой нет, из выдачи не пропадает: покупатель искал бы блюдо, которое вчера видел,
// и не находил его без объяснения.
func (a partnerAPI) ListMenu(w http.ResponseWriter, r *http.Request, params partner_v1.ListMenuParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, menuDefaultPageSize, menuMaxPageSize)
	if err != nil {
		refuseUnprocessableRequest(w, r, http.StatusBadRequest,
			"Некорректные параметры запроса", err.Error())

		return
	}

	page, err := a.menu.Page(r.Context(), application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		// Причина уходит в лог, а не площадке: наружу она рассказала бы об устройстве
		// коробки, а выбрать по ней ветку поведения площадка всё равно не может.
		// Синхронизация каталога от неудачного ответа не страдает: выгрузка применяется
		// целиком, и незаконченная оставляет площадке прежний каталог.
		a.log.ErrorContext(r.Context(), "menu request failed", slog.Any("error", err))

		problem.Write(r.Context(), w, http.StatusInternalServerError, internalProblem{
			Type:   problem.DefaultType,
			Title:  "Внутренняя ошибка",
			Status: http.StatusInternalServerError,
		})

		return
	}

	items := make([]partner_v1.MenuItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, contractItem(item))
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, partner_v1.MenuPage{
		Items: items,
		Page: partner_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
		},
	})
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

// refuseUnprocessableRequest отвечает площадке, что запрос не проходит проверку.
//
// Код причины у всех таких отказов один — `validation_failed`: в закрытом перечне
// контракта другого на непринятый запрос нет, и заводить его незачем — ветка поведения у
// площадки та же самая. Отказы по правилам предметной области собирают свой документ:
// у них есть и причина, и позиция, из-за которой отказан заказ.
func refuseUnprocessableRequest(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	refusePartnerRequest(w, r, status, partner_v1.ValidationFailed, title, detail)
}

// refusePartnerRequest отвечает площадке отказом в схеме партнёрского контракта: тип
// собран по нему же, поэтому тело ответа не может разойтись с контрактом.
func refusePartnerRequest(
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

// AcceptOrder принимает заказ, переданный площадкой по контракту.
//
// Синхронная проверка: коробка — источник истины по наличию и цене, и успешный ответ
// означает обязательство выполнить заказ, а не «мы посмотрим». Всё, что коробка выполнить
// не может, отдаётся отказом с машиночитаемой причиной, по которой площадка выбирает
// ветку поведения — обновить каталог, показать покупателю новую цену или увести его в
// другое заведение.
//
// Повторная отправка того же заказа опознаётся по ключу идемпотентности: второй заказ не
// заводится, остаток второй раз не списывается, а площадке отвечает результат первой
// попытки — `200` вместо `201`. Номер попытки приходит заголовком контракта и уходит в
// лог: по нему видно, что площадка ретраила и сколько раз, а поведение коробки от него не
// зависит — иначе повтор с потерянным номером готовился бы заново.
func (a partnerAPI) AcceptOrder(w http.ResponseWriter, r *http.Request, params partner_v1.AcceptOrderParams) {
	var body partner_v1.IncomingOrder
	if err := decodeBody(r, &body, "order_id", "address", "items", "total"); err != nil {
		a.refuse(w, r, err)

		return
	}

	items := make([]domain.RequestedItem, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, domain.RequestedItem{
			ExternalID: item.ExternalId,
			Quantity:   item.Quantity,
			Price:      domain.Money(item.Price),
		})
	}

	request, err := domain.NewOrderRequest(body.OrderId, params.IdempotencyKey,
		body.Address, items, domain.Money(body.Total), placedAt(body.PlacedAt))
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	order, accepted, err := a.orders.Accept(r.Context(), request)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	a.log.InfoContext(r.Context(), "order delivered by platform",
		slog.String("order_id", order.ID.String()),
		slog.String("platform_order_id", order.PlatformOrderID.String()),
		slog.Int("attempt", params.IdempotencyAttempt),
		slog.Bool("accepted_now", accepted),
	)

	// Заказ, заведённый этой доставкой, — `201`; повтор — `200` с тем же телом: по коду
	// ответа площадка видит, случилось ли что-то новое, а по телу — что именно у неё
	// приняли.
	status := http.StatusOK
	if accepted {
		status = http.StatusCreated
	}

	httpx.WriteJSON(r.Context(), w, status, partner_v1.OrderAcceptance{
		// Наружу уезжает идентификатор заказа площадки, а не собственный идентификатор
		// коробки: площадка сверяет ответ со своим заказом, а о внутренней нумерации
		// заведения ей знать нечего.
		OrderId:    order.PlatformOrderID,
		AcceptedAt: order.AcceptedAt,
	})
}

// CancelAcceptedOrder отменяет заказ по просьбе площадки.
//
// Так до заведения доезжает отмена покупателем: решение принято на площадке, а коробка
// снимает заказ с кухни и возвращает продукты в оборот. Отменить она соглашается, пока
// продукты не потрачены; с началом готовки — `409` с кодом `order_not_cancellable`, и
// дальше такой случай разбирает саппорт площадки, а не повтор вызова.
//
// Повторная просьба отменить уже отменённый заказ отвечает тем же `200` и тем же
// временем отмены: площадка повторяет вызов, не получив ответа, и ломаться на повторе
// приём не должен — а остаток второй раз в меню не возвращается.
//
// Заказ называется идентификатором площадки: своей нумерации коробки она не знает, и
// контракт спрашивает именно его.
func (a partnerAPI) CancelAcceptedOrder(w http.ResponseWriter, r *http.Request, orderID partner_v1.OrderId) {
	reason, err := cancellationReason(r)
	if err != nil {
		a.refuseCancellation(w, r, err)

		return
	}

	order, err := a.orders.Cancel(r.Context(), orderID, reason)
	if err != nil {
		a.refuseCancellation(w, r, err)

		return
	}

	a.log.InfoContext(r.Context(), "order cancelled by the platform",
		slog.String("order_id", order.ID.String()),
		slog.String("platform_order_id", order.PlatformOrderID.String()))

	httpx.WriteJSON(r.Context(), w, http.StatusOK, partner_v1.OrderCancellation{
		// Наружу уезжает идентификатор заказа площадки, а не собственный идентификатор
		// коробки: площадка сверяет ответ со своим заказом.
		OrderId:     order.PlatformOrderID,
		CancelledAt: order.UpdatedAt,
	})
}

// cancellationReason достаёт из запроса причину отмены, которую площадка передаёт
// оператору.
//
// Тела у просьбы может не быть вовсе — контракт его не требует: отмену объясняют, но
// объяснение не обязательно, и его отсутствие не повод не отменять заказ. Пустое тело
// разбирается в пустую причину, а не в отказ.
func cancellationReason(r *http.Request) (string, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", bodyError{Reason: "тело запроса: " + err.Error()}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}

	var body partner_v1.CancellationRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", bodyError{Reason: "тело запроса: " + err.Error()}
	}
	if body.Reason == nil {
		return "", nil
	}

	return *body.Reason, nil
}

// refuseCancellation отвечает на ошибку отмены заказа.
//
// Заказ, которого коробка не принимала, — `404`: площадка просит отменить то, чего у
// заведения нет, и молчаливое согласие с этим означало бы, что она считает отменённым
// заказ, о котором здесь не знают.
func (a partnerAPI) refuseCancellation(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid domain.InvalidOrderError
		badBody bodyError
	)

	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		refusePartnerRequest(w, r, http.StatusNotFound, partner_v1.OrderNotFound,
			"Заказ не найден", "заказа с таким идентификатором заведение не принимало")
	case errors.Is(err, domain.ErrOrderNotCancellable):
		refusePartnerRequest(w, r, http.StatusConflict, partner_v1.OrderNotCancellable,
			"Заказ уже нельзя отменить",
			"заведение начало готовить заказ: продукты потрачены — отмена только через саппорт")
	case errors.As(err, &invalid):
		refuseUnprocessableRequest(w, r, http.StatusBadRequest, "Некорректная отмена", invalid.Reason)
	case errors.As(err, &badBody):
		refuseUnprocessableRequest(w, r, http.StatusBadRequest, "Некорректная отмена", badBody.Reason)
	default:
		a.refuseByOurFault(w, r, err)
	}
}

// placedAt приводит время оформления заказа к значению: площадка не обязана его
// присылать, и «не сказали» здесь — это нулевое время, а не ошибка.
func placedAt(moment *time.Time) time.Time {
	if moment == nil {
		return time.Time{}
	}

	return *moment
}

// refuse отвечает на ошибку приёма заказа: коробка не берётся за него, площадка прислала
// сломанный вызов либо отказала сама коробка. Разбор в одном месте — обработчики
// повторяют не разбор ошибок, а только свой сценарий.
func (a partnerAPI) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var (
		refusal domain.OrderRefusedError
		invalid domain.InvalidOrderError
		badBody bodyError
	)

	switch {
	case errors.As(err, &refusal):
		a.refuseOrder(w, r, refusal)
	case errors.As(err, &invalid):
		refuseUnprocessableRequest(w, r, http.StatusBadRequest, "Некорректный заказ", invalid.Reason)
	case errors.As(err, &badBody):
		refuseUnprocessableRequest(w, r, http.StatusBadRequest, "Некорректный заказ", badBody.Reason)
	default:
		a.refuseByOurFault(w, r, err)
	}
}

// refuseOrder отвечает площадке отказом по правилам предметной области.
//
// Код ответа один на все причины — `409`: контракт обещает на приёме заказа только его,
// и различает причины машиночитаемое поле `code`, а не код ответа. Позиция, из-за которой
// отказан заказ, и её актуальная цена уезжают тем же документом: без них площадке
// пришлось бы гадать, что чинить в каталоге.
func (a partnerAPI) refuseOrder(w http.ResponseWriter, r *http.Request, refusal domain.OrderRefusedError) {
	code, described := refusalCode(refusal.Reason)
	if !described {
		a.refuseByOurFault(w, r, refusal)

		return
	}

	document := partner_v1.Problem{
		Type:   problem.DefaultType,
		Title:  refusalTitle(refusal.Reason),
		Status: http.StatusConflict,
		Detail: &refusal.Detail,
		Code:   code,
	}
	if refusal.ProductExternalID != "" {
		document.ProductExternalId = &refusal.ProductExternalID
	}
	if refusal.Reason == domain.PriceChanged {
		price := int64(refusal.CurrentPrice)
		document.CurrentPrice = &price
	}

	problem.Write(r.Context(), w, http.StatusConflict, document)
}

// refuseByOurFault отвечает на отказ по вине коробки.
func (a partnerAPI) refuseByOurFault(w http.ResponseWriter, r *http.Request, err error) {
	// Причина уходит в лог, а не площадке: наружу она рассказала бы об устройстве
	// коробки, а выбрать по ней ветку поведения площадка всё равно не может — ей
	// остаётся повторить доставку заказа с тем же ключом идемпотентности.
	a.log.ErrorContext(r.Context(), "partner request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, internalProblem{
		Type:   problem.DefaultType,
		Title:  "Внутренняя ошибка",
		Status: http.StatusInternalServerError,
	})
}

// refuseOversizedBody отвечает на запрос, тело которого больше допустимого.
func (a partnerAPI) refuseOversizedBody(w http.ResponseWriter, r *http.Request) {
	refuseUnprocessableRequest(w, r, http.StatusRequestEntityTooLarge, "Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes))
}

// refusalCode переводит причину отказа предметной области в код партнёрского контракта.
//
// Перевод явный, а не приведением строки: перечень кодов контракта закрыт, обе стороны
// вправе разбирать его как перечисление, и причина, которой в контракте нет, — это
// расхождение коробки с контрактом, а не ответ площадке.
func refusalCode(reason domain.RefusalReason) (partner_v1.ProblemCode, bool) {
	switch reason {
	case domain.RestaurantClosed:
		return partner_v1.RestaurantClosed, true
	case domain.UnknownProduct:
		return partner_v1.UnknownProduct, true
	case domain.OutOfStock:
		return partner_v1.OutOfStock, true
	case domain.PriceChanged:
		return partner_v1.PriceChanged, true
	default:
		return "", false
	}
}

// refusalTitle — короткое название отказа для человека, который читает лог площадки.
func refusalTitle(reason domain.RefusalReason) string {
	switch reason {
	case domain.RestaurantClosed:
		return "Заведение закрыто"
	case domain.UnknownProduct:
		return "Неизвестная позиция"
	case domain.OutOfStock:
		return "Позиции нет в наличии"
	case domain.PriceChanged:
		return "Цена изменилась"
	default:
		return "Заказ не принят"
	}
}
