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
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/admin_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// AdminAPIPrefix — префикс админского API площадки. Сами пути берутся из спецификации,
// здесь только то, что в ней вынесено в адрес сервера.
const AdminAPIPrefix = "/api/kitchen/v1/admin"

// adminAPI обслуживает админское API площадки: подключение заведений и правку их
// реквизитов.
//
// Защиты у этого API в MVP нет — так же, как её нет у клиентского: сотрудник называет
// себя тем же заголовком `User-Id`, что и покупатель. Это решение, а не упущение: цена
// его названа прямо в спецификации и в README, а закрывать ручки ролью в токене — работа
// эксплуатации, а не демонстрации. Партнёрское API закрыто ключом, потому что там за
// заголовком стоит другая организация, двигающая чужие заказы.
//
// Операции, до которых очередь ещё не дошла, отвечают `ErrNotImplemented`: маршрут
// существует потому, что существует контракт, а обработчик подставляется на своём этапе.
type adminAPI struct {
	onboarding *application.Onboarding
	log        *slog.Logger
}

// mountAdminAPI монтирует админское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountAdminAPI(r chi.Router, onboarding *application.Onboarding, log *slog.Logger) {
	api := adminAPI{onboarding: onboarding, log: log}

	handler := admin_v1.NewStrictHandlerWithOptions(api, nil, admin_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writeAdminRequestProblem,
		ResponseErrorHandlerFunc: api.writeAdminResponseProblem,
	})

	// Лимит тела вешается на админское API, а не на весь роутер: отказ отдаётся по схеме
	// админской спецификации, а у клиентского и партнёрского API схема своя. Он стоит
	// перед сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, refuseOversizedAdminBody))

		admin_v1.HandlerWithOptions(handler, admin_v1.ChiServerOptions{
			BaseURL:    AdminAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, отсутствие
			// обязательного заголовка. Контракт обещает на них `problem+json`, а не текст
			// по умолчанию.
			ErrorHandlerFunc: writeAdminRequestProblem,
		})
	})
}

// RegisterRestaurant подключает заведение и отдаёт выданный ему партнёрский ключ.
//
// Ключ виден один раз — в этом ответе: площадка хранит только его хеш и восстановить ключ
// не может. Потерянный ключ перевыпускается, а не вспоминается.
func (a adminAPI) RegisterRestaurant(
	ctx context.Context, request admin_v1.RegisterRestaurantRequestObject,
) (admin_v1.RegisterRestaurantResponseObject, error) {
	registered, err := a.onboarding.Register(ctx, request.Body.Name, request.Body.BaseUrl)

	var invalid domain.InvalidPartnerError

	switch {
	case errors.As(err, &invalid):
		return admin_v1.RegisterRestaurant400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: admin_v1.
				BadRequestApplicationProblemPlusJSONResponse(adminProblem(
					http.StatusBadRequest, admin_v1.ValidationFailed,
					"Некорректный запрос", invalid.Reason)),
		}, nil
	case err != nil:
		return nil, err
	}

	return admin_v1.RegisterRestaurant201JSONResponse{
		Id:         registered.Partner.ID,
		Name:       registered.Partner.Name,
		BaseUrl:    registered.Partner.BaseURL,
		IsActive:   registered.Partner.IsActive,
		PartnerKey: registered.Key,
	}, nil
}

// UpdateRestaurant правит реквизиты заведения: название, адрес интеграции и подключено ли
// оно.
//
// Правка частичная, как обещает контракт: применяется только то, что прислали. Пустое
// поле от неприсланного отличает сгенерированный тип — необязательные поля схемы
// приезжают указателями, поэтому обработчику не приходится разбирать тело самому.
func (a adminAPI) UpdateRestaurant(
	ctx context.Context, request admin_v1.UpdateRestaurantRequestObject,
) (admin_v1.UpdateRestaurantResponseObject, error) {
	partner, err := a.onboarding.Change(ctx, request.RestaurantId, application.Requisites{
		Name:     request.Body.Name,
		BaseURL:  request.Body.BaseUrl,
		IsActive: request.Body.IsActive,
	})

	var invalid domain.InvalidPartnerError

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		return admin_v1.UpdateRestaurant404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: admin_v1.
				NotFoundApplicationProblemPlusJSONResponse(adminProblem(
					http.StatusNotFound, admin_v1.RestaurantNotFound, "Заведение не найдено",
					"заведения с таким идентификатором на площадке нет")),
		}, nil
	case errors.As(err, &invalid):
		return admin_v1.UpdateRestaurant400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: admin_v1.
				BadRequestApplicationProblemPlusJSONResponse(adminProblem(
					http.StatusBadRequest, admin_v1.ValidationFailed,
					"Некорректный запрос", invalid.Reason)),
		}, nil
	case err != nil:
		return nil, err
	}

	return admin_v1.UpdateRestaurant200JSONResponse{
		Id:        partner.ID,
		Name:      partner.Name,
		BaseUrl:   partner.BaseURL,
		IsActive:  partner.IsActive,
		CreatedAt: partner.CreatedAt,
		UpdatedAt: partner.UpdatedAt,
	}, nil
}

// Отмена заказа саппортом описана контрактом, её обработчик подставляется своим этапом.

func (adminAPI) CancelOrderBySupport(
	context.Context, admin_v1.CancelOrderBySupportRequestObject,
) (admin_v1.CancelOrderBySupportResponseObject, error) {
	return nil, httpx.ErrNotImplemented
}

// adminProblem собирает отказ админского API. Тело — общая основа `problem+json`: своих
// полей у этих отказов нет, а тип сгенерирован по спецификации, поэтому ответ не может
// разойтись с контрактом.
func adminProblem(
	status int, code admin_v1.ProblemCode, title, detail string,
) admin_v1.ProblemBase {
	return admin_v1.ProblemBase{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	}
}

// writeAdminRequestProblem отвечает на неразобранный запрос — неверный uuid в пути,
// отсутствующий заголовок, неразбираемое тело.
func writeAdminRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	problem.Write(r.Context(), w, http.StatusBadRequest, adminProblem(
		http.StatusBadRequest, admin_v1.ValidationFailed,
		"Некорректные параметры запроса", err.Error()))
}

// refuseOversizedAdminBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет.
func refuseOversizedAdminBody(w http.ResponseWriter, r *http.Request) {
	problem.Write(r.Context(), w, http.StatusRequestEntityTooLarge, adminProblem(
		http.StatusRequestEntityTooLarge, admin_v1.ValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes)))
}

// writeAdminResponseProblem отвечает, когда обработчик вернул ошибку. Операция, описанная
// контрактом, но ещё не реализованная, — это `501`; всё остальное `500`.
//
// Причина отказа по вине площадки уходит в лог, а не администратору: наружу она
// рассказала бы об устройстве службы, а выбрать по ней ветку поведения нельзя — остаётся
// повторить запрос позже.
func (a adminAPI) writeAdminResponseProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, httpx.ErrNotImplemented) {
		problem.Write(r.Context(), w, http.StatusNotImplemented, adminProblem(
			http.StatusNotImplemented, admin_v1.NotImplemented,
			"Операция ещё не реализована", err.Error()))

		return
	}

	a.log.ErrorContext(r.Context(), "admin request failed", slog.Any("error", err))

	problem.Write(r.Context(), w, http.StatusInternalServerError, adminProblem(
		http.StatusInternalServerError, admin_v1.InternalError,
		"Внутренняя ошибка площадки", "повторите запрос позже"))
}
