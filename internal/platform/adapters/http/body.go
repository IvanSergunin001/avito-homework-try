package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decodeBody разбирает тело запроса и убеждается, что в нём есть перечисленные поля.
//
// Обязательность полей объявлена спецификацией, но кодогенератор её не проверяет, а
// нулевое значение пропущенного поля осмысленно: заказ без адреса разобрался бы в пустую
// строку, а правка заведения без флага подключения тихо отключила бы его.
//
// Неизвестные поля не отвергаются: клиент новее службы не должен ломаться о поле, о
// котором она ещё не знает. По той же причине не отвергается и поле, которого в схеме
// нет вовсе, — например, присланная клиентом сумма заказа: площадка её просто не читает,
// и посчитанной суммы это не касается.
//
// Тело читается целиком, но его размер уже ограничен обвязкой поверхности.
func decodeBody(r *http.Request, body any, required ...string) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.New("тело запроса: " + err.Error())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("тело запроса: " + err.Error())
	}

	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return errors.New(field + ": обязательное поле")
		}
	}

	if err := json.Unmarshal(raw, body); err != nil {
		return errors.New("тело запроса: " + err.Error())
	}

	return nil
}
