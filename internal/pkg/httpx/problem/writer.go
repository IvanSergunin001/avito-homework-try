package problem

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
)

// Codes — коды тех отказов, которые есть у каждого API одинаковыми. Перечень кодов закрыт
// общей схемой (`api/common`), но тип у каждого API свой, сгенерированный по его
// спецификации, — поэтому коды приезжают сюда значениями, а не константами пакета.
type Codes[C ~string] struct {
	// ValidationFailed — запрос не принят: неверные параметры, неразбираемое или слишком
	// большое тело.
	ValidationFailed C
	// NotImplemented — операция описана контрактом, но обработчика у неё ещё нет.
	NotImplemented C
	// InternalError — служба не справилась сама.
	InternalError C
}

// Writer отвечает отказами одного API.
//
// Четыре отказа — «неразобранный запрос», «слишком большое тело», «операции ещё нет» и
// «внутренняя ошибка» — устроены одинаково у всех пяти поверхностей проекта и до этого
// типа жили в каждом адаптере своей копией. Отличались копии только сгенерированными
// типами, поэтому именно они и вынесены в параметры типа: B — тело отказа этого API,
// C — его перечисление кодов.
//
// Цена копий была не в объёме, а в том, что правка формы отказа требовала обойти все
// адаптеры и в двух из них была забыта: тело, оборвавшееся на пределе `MaxBytesReader`,
// отдавалось как `400` вместо `413`. Здесь эта ветка существует ровно в одном месте.
//
// Поля заполняются литералом при сборке адаптера: у Writer нет состояния, которое нужно
// прятать, а имена полей рядом со значениями читаются лучше конструктора на пять
// аргументов.
type Writer[B any, C ~string] struct {
	// Body собирает тело отказа типами того API, которым отвечаем: раз тип сгенерирован
	// по спецификации, тело не может разойтись с контрактом.
	Body func(status int, code C, title, detail string) B
	// Codes — коды общих отказов в перечислении этого API.
	Codes Codes[C]
	// MaxRequestBytes — предел на тело запроса, который называет отказ `413`. Тот же, что
	// стоит в httpx.LimitRequestBody этой поверхности.
	MaxRequestBytes int64
	// FailureTitle — заголовок отказа по вине службы: у площадки и у коробки он свой.
	FailureTitle string
	// Surface — имя поверхности в логе: по нему видно, чей отказ, при том что сообщение
	// у всех одно. Отдельным полем, а не отдельным сообщением, потому что читают лог
	// запросом по полю, а не глазами по строке.
	Surface string
}

// Write отвечает отказом в схеме этого API.
func (p Writer[B, C]) Write(
	w http.ResponseWriter, r *http.Request, status int, code C, title, detail string,
) {
	Write(r.Context(), w, status, p.Body(status, code, title, detail))
}

// Validation собирает тело отказа по неверному запросу. Оно нужно отдельно от Write:
// сгенерированные типы ответов носят его внутри себя, и обработчик возвращает такой
// ответ, а не пишет его сам.
func (p Writer[B, C]) Validation(err error) B {
	return p.Body(http.StatusBadRequest, p.Codes.ValidationFailed,
		"Некорректные параметры запроса", err.Error())
}

// BadRequest отвечает на неразобранный запрос — неверный uuid в пути, нечисловой `limit`,
// неразбираемое тело.
//
// Тело, оборвавшееся на пределе, приезжает сюда же: до middleware ошибка MaxBytesReader
// уже не поднимается, её видит тот, кто разбирает тело. Отдаётся оно тем же `413`, что и
// объявленная длина сверх предела, — знать, объявил клиент длину честно или прислал тело
// без длины вовсе, ему незачем.
func (p Writer[B, C]) BadRequest(w http.ResponseWriter, r *http.Request, err error) {
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		p.Oversized(w, r)

		return
	}

	Write(r.Context(), w, http.StatusBadRequest, p.Validation(err))
}

// Oversized отвечает на запрос, тело которого больше допустимого.
//
// Код причины — `validation_failed`: запрос не прошёл проверку, и другого кода на это в
// закрытом перечне спецификации нет. Заводить новый код ради размера тела незачем —
// ветка поведения у клиента та же самая, что и при любом непринятом запросе.
func (p Writer[B, C]) Oversized(w http.ResponseWriter, r *http.Request) {
	p.Write(w, r, http.StatusRequestEntityTooLarge, p.Codes.ValidationFailed,
		"Слишком большое тело запроса",
		fmt.Sprintf("тело запроса ограничено %d байтами", p.MaxRequestBytes))
}

// Failure отвечает, когда обработчик вернул ошибку. Операция, описанная контрактом, но
// ещё не реализованная, — это `501`; всё остальное `500`.
//
// Причина отказа по вине службы уходит в лог, а не наружу: там она рассказала бы об
// устройстве службы, а выбрать по ней ветку поведения клиент всё равно не может — ему
// остаётся повторить запрос позже.
func (p Writer[B, C]) Failure(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, httpx.ErrNotImplemented) {
		p.Write(w, r, http.StatusNotImplemented, p.Codes.NotImplemented,
			"Операция ещё не реализована", err.Error())

		return
	}

	log.ErrorContext(r.Context(), "request failed",
		slog.String("api", p.Surface), slog.Any("error", err))

	p.Write(w, r, http.StatusInternalServerError, p.Codes.InternalError,
		p.FailureTitle, "повторите запрос позже")
}
