package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger пишет по строке на завершённый запрос: метод, путь, код ответа,
// размер тела и длительность. Идентификатор запроса берётся из middleware.RequestID,
// чтобы строки одного запроса можно было связать между службами.
//
// Проба здоровья пишется уровнем debug: её дёргают раз в несколько секунд, и на обычном
// уровне она вытеснила бы из логов всё остальное.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			started := time.Now()

			next.ServeHTTP(ww, r)

			level := slog.LevelInfo
			if r.URL.Path == HealthPath {
				level = slog.LevelDebug
			}

			// Паника до сюда не долетает: Recoverer стоит ближе к обработчику и
			// превращает её в ответ 500, который мы и записываем.
			log.LogAttrs(r.Context(), level, "request handled",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("took", time.Since(started)),
				slog.String("request_id", middleware.GetReqID(r.Context())),
			)
		})
	}
}
