package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/IvanSergunin001/Test157/internal/pkg/config"
)

// Serve поднимает HTTP-сервер по настройкам cfg и блокируется, пока не отменят ctx. После
// отмены даёт начатым запросам cfg.ShutdownTimeout на завершение: в Compose и в оркестраторе
// остановка приходит сигналом, и обрывать на нём принятый заказ не за что.
//
// Настройки передаются одним значением, а не адресом и таймаутом по отдельности: это одна
// и та же тройка полей, которая всё равно ездит вместе от разбора окружения до сервера.
func Serve(ctx context.Context, log *slog.Logger, cfg config.Config, handler http.Handler) error {
	addr := cfg.Addr()

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
		log.InfoContext(ctx, "http server stopping", slog.Duration("timeout", cfg.ShutdownTimeout))
	}

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(stopCtx); err != nil {
		return err
	}

	return <-errs
}
