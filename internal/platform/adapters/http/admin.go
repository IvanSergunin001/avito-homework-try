package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx/problem"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/admin_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// AdminAPIPrefix — префикс админского API площадки. Сами пути берутся из спецификации,
// здесь только то, что в ней вынесено в адрес сервера.
const AdminAPIPrefix = "/api/kitchen/v1/admin"

// adminAPI обслуживает админское API площадки: подключение заведений, правку их
// реквизитов и отмену заказа саппортом.
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
	onboarding   *application.Onboarding
	cancellation *application.Cancellation
	log          *slog.Logger
}

// mountAdminAPI монтирует админское API на роутер площадки. Список маршрутов целиком
// сгенерирован по спецификации: руками он не переписывается и разойтись с контрактом
// не может.
func mountAdminAPI(
	r chi.Router, onboarding *application.Onboarding,
	cancellation *application.Cancellation, log *slog.Logger,
) {
	api := adminAPI{onboarding: onboarding, cancellation: cancellation, log: log}

	handler := admin_v1.NewStrictHandlerWithOptions(api, nil, admin_v1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  adminProblems.BadRequest,
		ResponseErrorHandlerFunc: api.writeAdminFailure,
	})

	// Лимит тела вешается на админское API, а не на весь роутер: отказ отдаётся по схеме
	// админской спецификации, а у клиентского и партнёрского API схема своя. Он стоит
	// перед сгенерированными маршрутами, потому что размер тела — это то, что проверяется
	// раньше всего остального: разбор параметров и тела уже читает запрос.
	r.Group(func(r chi.Router) {
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, adminProblems.Oversized))

		admin_v1.HandlerWithOptions(handler, admin_v1.ChiServerOptions{
			BaseURL:    AdminAPIPrefix,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути, отсутствие
			// обязательного заголовка. Контракт обещает на них `problem+json`, а не текст
			// по умолчанию.
			ErrorHandlerFunc: adminProblems.BadRequest,
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
				BadRequestApplicationProblemPlusJSONResponse(adminProblemBody(
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
				NotFoundApplicationProblemPlusJSONResponse(adminProblemBody(
					http.StatusNotFound, admin_v1.RestaurantNotFound, "Заведение не найдено",
					"заведения с таким идентификатором на площадке нет")),
		}, nil
	case errors.As(err, &invalid):
		return admin_v1.UpdateRestaurant400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: admin_v1.
				BadRequestApplicationProblemPlusJSONResponse(adminProblemBody(
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

// adminProblemBody собирает тело отказа админского API — общую основу `problem+json`.
// Своих полей у этих отказов нет, а тип сгенерирован по спецификации, поэтому ответ не
// может разойтись с контрактом.
func adminProblemBody(
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

// adminProblems — отказы админского API. Форма у них та же, что у остальных четырёх
// поверхностей проекта, поэтому она живёт одна на всех (problem.Writer), а здесь названо
// только то, чем админское API от них отличается: сгенерированные типы, коды и тексты.
var adminProblems = problem.Writer[admin_v1.ProblemBase, admin_v1.ProblemCode]{
	Body: adminProblemBody,
	Codes: problem.Codes[admin_v1.ProblemCode]{
		ValidationFailed: admin_v1.ValidationFailed,
		NotImplemented:   admin_v1.NotImplemented,
		InternalError:    admin_v1.InternalError,
	},
	MaxRequestBytes: maxRequestBodyBytes,
	FailureTitle:    "Внутренняя ошибка площадки",
	Surface:         "admin",
}

// writeAdminFailure отвечает, когда обработчик вернул ошибку: `501` на операцию,
// описанную контрактом, но ещё не реализованную, и `500` на всё остальное. Форма отказа
// общая, а логгер у каждого адаптера свой — за ним метод и заводится.
func (a adminAPI) writeAdminFailure(w http.ResponseWriter, r *http.Request, err error) {
	adminProblems.Failure(a.log, w, r, err)
}
