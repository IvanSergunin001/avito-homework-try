package http

import (
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

	menu *application.Menu
	log  *slog.Logger
}

// mountPartnerAPI монтирует партнёрский контракт на роутер коробки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountPartnerAPI(r chi.Router, menu *application.Menu, log *slog.Logger) {
	partner_v1.HandlerWithOptions(partnerAPI{menu: menu, log: log}, partner_v1.ChiServerOptions{
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

// ListMenu отдаёт страницу меню заведения — то самое меню, которое ведёт оператор.
//
// Наружу уезжает меньше, чем видит оператор: остаток и отметка стоп-листа — внутренняя
// кухня заведения, площадке достаточно ответа «заказать можно или нельзя». Позиция,
// которой нет, из выдачи не пропадает: покупатель искал бы блюдо, которое вчера видел,
// и не находил его без объяснения.
func (a partnerAPI) ListMenu(w http.ResponseWriter, r *http.Request, params partner_v1.ListMenuParams) {
	paging, err := httpx.ResolvePaging(params.Limit, params.Offset, menuDefaultPageSize, menuMaxPageSize)
	if err != nil {
		writePartnerProblem(w, r, http.StatusBadRequest, partner_v1.ValidationFailed,
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
