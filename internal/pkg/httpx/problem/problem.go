// Package problem пишет отказы служб в формате `application/problem+json`
// (RFC 7807, актуализирован RFC 9457).
//
// Общего типа документа здесь нет намеренно: у каждого API свой сгенерированный тип
// ошибки, хотя общая часть схемы и живёт одна на всех (`api/common`). Пакету достаточно
// отвечать за транспорт: заголовок, код ответа и запись тела — и за четыре отказа,
// одинаковых у всех API, которые до этого пакета жили копией в каждом адаптере
// (см. Writer).
//
// Лежит он внутри httpx, а не рядом с ним: отказ — это часть HTTP-ответа, а не
// самостоятельное понятие, и Writer сводит к одному месту ветку «оборвавшееся на пределе
// тело — это 413», где предел ставит httpx.LimitRequestBody.
package problem

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// MediaType — тип содержимого отказа.
const MediaType = "application/problem+json"

// DefaultType — значение поля `type`, когда отдельного URI у проблемы нет: причину несёт
// машиночитаемое поле `code`, и заводить страницу документации под каждый код незачем.
const DefaultType = "about:blank"

// Write отправляет отказ. document — модель ошибки того API, которым отвечаем:
// раз тип сгенерирован по спецификации, тело ответа не может разойтись с контрактом.
func Write(ctx context.Context, w http.ResponseWriter, status int, document any) {
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(document); err != nil {
		slog.ErrorContext(ctx, "write problem response", slog.Any("error", err))
	}
}
