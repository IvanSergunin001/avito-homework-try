package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// MediaTypeJSON — тип содержимого успешных ответов обеих служб.
const MediaTypeJSON = "application/json"

// WriteJSON отправляет body как JSON. Ошибка записи уже не поправима — заголовки ушли
// клиенту, — поэтому она только пишется в лог: подменить ответ на 500 в этот момент
// нельзя, а молчать о сорванной записи не стоит.
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", MediaTypeJSON)
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(ctx, "write json response", slog.Any("error", err))
	}
}
