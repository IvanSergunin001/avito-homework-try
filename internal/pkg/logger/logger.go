// Package logger собирает структурированный логгер служб.
package logger

import (
	"io"
	"log/slog"
)

// New возвращает JSON-логгер уровня level. Формат один на обе службы: вывод Compose —
// это общий поток, и разбирать его удобнее машиной, а не глазами.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
