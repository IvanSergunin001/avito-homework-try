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
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/admin_v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// adminBaseURL — префикс админской поверхности. Сами пути берутся из спецификации, здесь
// только то, что в ней вынесено в адрес сервера.
const adminBaseURL = "/api/admin/v1"

// adminAPI обслуживает админскую поверхность площадки: подключение заведений и правку их
// реквизитов.
//
// Встроенная сгенерированная заглушка отвечает `501` на операции, до которых очередь ещё
// не дошла: маршрут существует потому, что существует контракт, а обработчик
// подставляется на своём этапе и перекрывает заглушку.
type adminAPI struct {
	admin_v1.Unimplemented

	onboarding *application.Onboarding
	log        *slog.Logger
}

// mountAdminAPI монтирует админскую поверхность на роутер площадки. Список маршрутов
// целиком сгенерирован по спецификации: руками он не переписывается и разойтись
// с контрактом не может.
func mountAdminAPI(r chi.Router, access guard, onboarding *application.Onboarding, log *slog.Logger) {
	api := adminAPI{onboarding: onboarding, log: log}

	r.Group(func(r chi.Router) {
		// Проверка токена стоит перед всем остальным, включая разбор тела: посторонний не
		// должен даже узнать, что запрос неверно составлен, — ответ «у вас опечатка в
		// поле» рассказывает о поверхности больше, чем ответ «вас сюда не пускают».
		r.Use(access.require(application.RoleAdmin, adminRefusals))
		r.Use(httpx.LimitRequestBody(maxRequestBodyBytes, api.refuseOversizedBody))

		admin_v1.HandlerWithOptions(api, admin_v1.ChiServerOptions{
			BaseURL:    adminBaseURL,
			BaseRouter: r,
			// Сюда попадают ошибки разбора параметров: неверный uuid в пути. Контракт
			// обещает на них `problem+json`, а не текст по умолчанию.
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeAdminProblem(w, r, http.StatusBadRequest, admin_v1.ProblemCodeValidationFailed,
					"Некорректные параметры запроса", err.Error())
			},
		})
	})
}

// RegisterRestaurant подключает заведение и отдаёт выданный ему партнёрский ключ.
//
// Ключ виден один раз — в этом ответе: площадка хранит только его хеш и восстановить ключ
// не может. Потерянный ключ перевыпускается, а не вспоминается.
func (a adminAPI) RegisterRestaurant(w http.ResponseWriter, r *http.Request) {
	var body admin_v1.NewRestaurant
	if err := decodeAdminBody(r, &body, "name", "base_url"); err != nil {
		a.refuse(w, r, err)

		return
	}

	registered, err := a.onboarding.Register(r.Context(), body.Name, body.BaseUrl)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusCreated, admin_v1.RegisteredRestaurant{
		Id:         registered.Partner.ID,
		Name:       registered.Partner.Name,
		BaseUrl:    registered.Partner.BaseURL,
		IsActive:   registered.Partner.IsActive,
		PartnerKey: registered.Key,
	})
}

// UpdateRestaurant заменяет реквизиты заведения: название, адрес интеграции и подключено
// ли оно.
//
// Замена целиком, а не правка отдельных полей: у реквизитов нет состояния, которое
// администратор менял бы «не глядя на остальные», а частичная правка требовала бы
// отличать «поле не прислали» от «поле обнулили».
func (a adminAPI) UpdateRestaurant(w http.ResponseWriter, r *http.Request, restaurantID admin_v1.RestaurantId) {
	var body admin_v1.RestaurantUpdate
	if err := decodeAdminBody(r, &body, "name", "base_url", "is_active"); err != nil {
		a.refuse(w, r, err)

		return
	}

	partner, err := a.onboarding.Change(r.Context(), restaurantID, body.Name, body.BaseUrl, body.IsActive)
	if err != nil {
		a.refuse(w, r, err)

		return
	}

	httpx.WriteJSON(r.Context(), w, http.StatusOK, admin_v1.Restaurant{
		Id:       partner.ID,
		Name:     partner.Name,
		BaseUrl:  partner.BaseURL,
		IsActive: partner.IsActive,
	})
}

// decodeAdminBody разбирает тело запроса и убеждается, что в нём есть перечисленные поля.
//
// Обязательность полей объявлена спецификацией, но кодогенератор её не проверяет, а у
// флага подключения значение по умолчанию — осмысленное: правка без `is_active` тихо
// отключила бы заведение, и узнал бы об этом не администратор, а покупатель, не нашедший
// заведение на витрине.
//
// Неизвестные поля не отвергаются: клиент новее службы не должен ломаться о поле, о
// котором она ещё не знает. Тело читается целиком, но его размер уже ограничен обвязкой
// поверхности.
func decodeAdminBody(r *http.Request, body any, required ...string) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return domain.InvalidPartnerError{Reason: "тело запроса: " + err.Error()}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.InvalidPartnerError{Reason: "тело запроса: " + err.Error()}
	}

	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return domain.InvalidPartnerError{Reason: field + ": обязательное поле"}
		}
	}

	if err := json.Unmarshal(raw, body); err != nil {
		return domain.InvalidPartnerError{Reason: "тело запроса: " + err.Error()}
	}

	return nil
}

// refuse отвечает на ошибку сценария: заведения нет, реквизиты описаны неверно либо
// отказала сама площадка. Разбор в одном месте — обработчики повторяют не разбор ошибок,
// а только свой сценарий.
func (a adminAPI) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var invalid domain.InvalidPartnerError

	switch {
	case errors.Is(err, domain.ErrRestaurantNotFound):
		writeAdminProblem(w, r, http.StatusNotFound, admin_v1.ProblemCodeRestaurantNotFound,
			"Заведение не найдено", "заведения с таким идентификатором на площадке нет")
	case errors.As(err, &invalid):
		writeAdminProblem(w, r, http.StatusBadRequest, admin_v1.ProblemCodeValidationFailed,
			"Некорректный запрос", invalid.Reason)
	default:
		writeInternalFailure(a.log, w, r, err)
	}
}

// refuseOversizedBody отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет.
func (a adminAPI) refuseOversizedBody(w http.ResponseWriter, r *http.Request) {
	writeAdminProblem(w, r, http.StatusRequestEntityTooLarge, admin_v1.ProblemCodeValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", maxRequestBodyBytes))
}

// adminRefusals — отказы в доступе к админской поверхности в её собственной схеме.
var adminRefusals = accessRefusals{
	unauthorized: func(w http.ResponseWriter, r *http.Request, detail string) {
		writeAdminProblem(w, r, http.StatusUnauthorized, admin_v1.ProblemCodeUnauthorized,
			"Требуется админский токен", detail)
	},
	forbidden: func(w http.ResponseWriter, r *http.Request, detail string) {
		writeAdminProblem(w, r, http.StatusForbidden, admin_v1.ProblemCodeForbidden,
			"Админская операция", detail)
	},
}

// writeAdminProblem отвечает отказом в схеме админской спецификации: тип сгенерирован по
// ней же, поэтому тело ответа не может разойтись с контрактом.
func writeAdminProblem(
	w http.ResponseWriter, r *http.Request, status int, code admin_v1.ProblemCode, title, detail string,
) {
	problem.Write(r.Context(), w, status, admin_v1.Problem{
		Type:   problem.DefaultType,
		Title:  title,
		Status: status,
		Detail: &detail,
		Code:   code,
	})
}
