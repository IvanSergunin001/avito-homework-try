package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Serve поднимает HTTP-сервер и блокируется, пока не отменят ctx. После отмены даёт
// начатым запросам shutdownTimeout на завершение: в Compose и в оркестраторе остановка
// приходит сигналом, и обрывать на нём принятый заказ не за что.
func Serve(ctx context.Context, log *slog.Logger, addr string, handler http.Handler, shutdownTimeout time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "http server started", slog.String("addr", addr))

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
		close(errs)
	}()

	select {
	case err := <-errs:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		log.InfoContext(ctx, "http server stopping", slog.Duration("timeout", shutdownTimeout))
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(stopCtx); err != nil {
		return err
	}

	return <-errs
}
