package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/problem"
	"github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http/operator_v1"
	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

const (
	// OperatorAPIPrefix — префикс рабочего места оператора. Партнёрский контракт коробка
	// отдаёт под `/api/kitchen/v1/restaurant`, а рабочее место стоит сверх контракта и
	// живёт своим циклом, поэтому у него свой сегмент — вложенный, как и папка спеки.
	OperatorAPIPrefix = PartnerAPIPrefix + "/operator"

	// Границы страницы меню повторяют объявленные в спецификации рабочего места.
	operatorDefaultPageSize = 20
	operatorMaxPageSize     = 100
)

// operatorAPI — рабочее место оператора заведения: меню, стоп-лист и остатки. Это то,
// чем заведение без своих систем заменяет собственный бэкенд, — площадка об этом API
// не знает и видит только партнёрскую выдачу меню.
type operatorAPI struct {
	menu *application.Menu
	log  *slog.Logger
}

// mountOperatorAPI монтирует рабочее место оператора на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с ней не может.
func mountOperatorAPI(r chi.Router, menu *application.Menu, log *slog.Logger) {
	api := operatorAPI{menu: menu, log: log}

	// Лимит тела вешается на рабочее место, а не на весь роутер: отказ отдаётся по схеме
	// его спецификации, а у партнёрского контракта схема своя. Он стоит перед
	// сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, api.refuseOversizedBody))

		operator_v1.HandlerWithOptions(api, operator_v1.ChiServerOptions{
			BaseURL:    OperatorAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, нечисловой
			// `limit`. Спецификация обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeOperatorProblem(w, r, http.StatusBadRequest, operator_v1.ValidationFailed,
					"Некорректные параметры запроса", err.Error())
			},
		})
	})
}

// ListMenuItems отдаёт страницу меню глазами оператора: с остатком и отметкой стоп-листа.
func (a operatorAPI) ListMenuItems(
	w http.ResponseWriter, r *http.Request, params operator_v1.ListMenuItemsParams,
) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, operatorDefaultPageSize, operatorMaxPageSize)
	if err != nil {
		writeOperatorProblem(w, r, http.StatusBadRequest, operator_v1.ValidationFailed,
			"Некорректные параметры запроса", err.Error())

		return
	}

	page, err := a.menu.Page(r.Context(), application.Window{Limit: paging.Limit, Offset: paging.Offset})
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	items := make([]operator_v1.MenuItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, operatorCard(item))
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, operator_v1.MenuItemPage{
		Items: items,
		Page: operator_v1.PageMeta{
			Limit:  paging.Limit,
			Offset: paging.Offset,
			Total:  page.Total,
		},
	})
}

// AddMenuItem заводит позицию меню.
func (a operatorAPI) AddMenuItem(w http.ResponseWriter, r *http.Request) {
	var body operator_v1.NewMenuItem
	if err := decodeBody(r, &body, "name", "price", "stock"); err != nil {
		a.refuse(w, r, err)

		return
	}

	card, err := domain.NewMenuCard(body.Name, describe(body.Description), domain.Money(body.Price))
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	stock, err := domain.NewStock(body.Stock)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	item, err := a.menu.Add(r.Context(), card, stock)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusCreated, operatorCard(item))
}

// UpdateMenuItem заменяет карточку позиции целиком.
func (a operatorAPI) UpdateMenuItem(w http.ResponseWriter, r *http.Request, itemID operator_v1.MenuItemId) {
	var body operator_v1.MenuItemUpdate
	if err := decodeBody(r, &body, "name", "price"); err != nil {
		a.refuse(w, r, err)

		return
	}

	card, err := domain.NewMenuCard(body.Name, describe(body.Description), domain.Money(body.Price))
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	item, err := a.menu.Edit(r.Context(), itemID, card)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, operatorCard(item))
}

// RemoveMenuItem убирает позицию из меню.
//
// Ответ без тела: у удалённой позиции нет состояния, о котором стоило бы рассказать.
func (a operatorAPI) RemoveMenuItem(w http.ResponseWriter, r *http.Request, itemID operator_v1.MenuItemId) {
	if err := a.menu.Remove(r.Context(), itemID); err != nil {
		a.refuse(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SetStopListMark отмечает позицию как отсутствующую или снимает отметку.
func (a operatorAPI) SetStopListMark(w http.ResponseWriter, r *http.Request, itemID operator_v1.MenuItemId) {
	var body operator_v1.StopListMark
	if err := decodeBody(r, &body, "in_stop_list"); err != nil {
		a.refuse(w, r, err)

		return
	}

	item, err := a.menu.MarkStopList(r.Context(), itemID, body.InStopList)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, operatorCard(item))
}

// SetStock записывает остаток позиции.
func (a operatorAPI) SetStock(w http.ResponseWriter, r *http.Request, itemID operator_v1.MenuItemId) {
	var body operator_v1.StockUpdate
	if err := decodeBody(r, &body, "stock"); err != nil {
		a.refuse(w, r, err)

		return
	}

	stock, err := domain.NewStock(body.Stock)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	item, err := a.menu.SetStock(r.Context(), itemID, stock)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, operatorCard(item))
}

// operatorCard переводит позицию в схему рабочего места.
//
// Доступность считается здесь же, а не хранится: оператор видит последствие своей правки
// сразу — иначе ему пришлось бы держать правило «нет отметки и остаток больше нуля» в
// голове и сверять две колонки глазами.
func operatorCard(item domain.MenuItem) operator_v1.MenuItem {
	card := operator_v1.MenuItem{
		Id:          item.ID,
		Name:        item.Name,
		Price:       int64(item.Price),
		Stock:       int(item.Stock),
		InStopList:  item.InStopList,
		IsAvailable: item.IsAvailable(),
	}

	// Описания у позиции может не быть — тогда поля нет и в ответе: пустая строка
	// сообщила бы, что описание есть и оно пустое.
	if item.Description != "" {
		card.Description = &item.Description
	}

	return card
}

// describe приводит описание из тела запроса к строке: его могли не прислать вовсе, и
// «нет описания» и «описание пустое» — для позиции меню одно и то же.
func describe(description *string) string {
	if description == nil {
		return ""
	}

	return *description
}

// decodeBody разбирает тело запроса и убеждается, что в нём есть перечисленные поля.
//
// Обязательность полей объявлена спецификацией, но кодогенератор её не проверяет, а у
// цены, остатка и отметки стоп-листа значение по умолчанию — осмысленное: запрос без
// цены завёл бы бесплатную позицию, без остатка — недоступную, а без отметки снял бы
// позицию со стопа. Об этом оператор узнал бы от покупателя, а не от коробки.
//
// Неизвестные поля не отвергаются: клиент новее службы не должен ломаться о поле, о
// котором она ещё не знает, — а всё, что служба обещала принять, описано спецификацией.
// Тело читается целиком, но его размер уже ограничен обвязкой API.
func decodeBody(r *http.Request, body any, required ...string) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return domain.InvalidMenuItemError{Reason: "тело запроса: " + err.Error()}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.InvalidMenuItemError{Reason: "тело запроса: " + err.Error()}
	}

	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return domain.InvalidMenuItemError{Reason: field + ": обязательное поле"}
		}
	}

	if err := json.Unmarshal(raw, body); err != nil {
		return domain.InvalidMenuItemError{Reason: "тело запроса: " + err.Error()}
	}

	return nil
}

// refuse отвечает на ошибку сценария: позиции нет, оператор описал её неверно либо
// отказала сама коробка. Разбор в одном месте — обработчики повторяют не разбор ошибок,
// а только свой сценарий.
func (a operatorAPI) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var invalid domain.InvalidMenuItemError

	switch {
	case errors.Is(err, domain.ErrMenuItemNotFound):
		writeOperatorProblem(w, r, http.StatusNotFound, operator_v1.MenuItemNotFound,
			"Позиция не найдена", "позиции с таким идентификатором в меню нет")
	case errors.As(err, &invalid):
		writeOperatorProblem(w, r, http.StatusBadRequest, operator_v1.ValidationFailed,
			"Некорректный запрос", invalid.Reason)
	default:
		// Причина уходит в лог, а не оператору: наружу она рассказала бы об устройстве
		// службы, а выбрать по ней ветку поведения оператор всё равно не может — ему
		// остаётся повторить запрос позже.
		a.log.ErrorContext(r.Context(), "operator request failed", slog.Any("error", err))

		writeOperatorProblem(w, r, http.StatusInternalServerError, operator_v1.InternalError,
			"Внутренняя ошибка коробки", "повторите запрос позже")
	}
}

// refuseOversizedBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет. Заводить новый код ради размера тела незачем —
// ветка поведения у клиента та же самая, что и при любом непринятом запросе.
func (a operatorAPI) refuseOversizedBody(w http.ResponseWriter, r *http.Request) {
	writeOperatorProblem(w, r, http.StatusRequestEntityTooLarge, operator_v1.ValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes))
}

// writeOperatorProblem отвечает отказом рабочего места. Тело — общая основа
// `problem+json`: своих полей у этих отказов нет, а тип сгенерирован по спецификации,
// поэтому ответ не может разойтись с обещанным.
func writeOperatorProblem(
	w http.ResponseWriter, r *http.Request, status int, code operator_v1.ProblemCode, title, detail string,
) {
	problem.Write(r.Context(), w, status, operator_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
