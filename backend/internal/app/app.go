// Package app — composition root: единственное место, где создаются
// зависимости и собирается граф. Остальные пакеты получают всё через
// конструкторы и ничего не создают сами.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/config"
	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpserver"
	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
)

// App — собранное приложение: конфиг, логгер, HTTP-сервер и ресурсы,
// которые нужно закрыть при остановке.
type App struct {
	cfg    config.Config
	log    *slog.Logger
	probe  *health.Probe
	server *httpserver.Server

	// Ресурсы, которые нужно закрыть при остановке, в порядке создания.
	// Закрываются в обратном порядке, как деструкторы членов класса в C++.
	// Первый — пул Postgres: он закрывается после остановки HTTP-сервера.
	closers []io.Closer
}

// New собирает приложение. Если сборка падает на середине, уже созданные
// ресурсы закрываются (именованный err + defer).
//
// ctx ограничивает только сборку (подключение к БД): отмена по сигналу во
// время старта прерывает его, а не ждёт таймаутов.
func New(ctx context.Context, cfg config.Config, logOut io.Writer) (a *App, err error) {
	a = &App{cfg: cfg}
	defer func() {
		if err != nil {
			err = errors.Join(err, a.closeAll())
			a = nil
		}
	}()

	a.log = newLogger(cfg, logOut)
	a.log.Info("starting", "config", cfg) // cfg логируется через LogValue (allowlist)

	db, err := postgres.New(ctx, postgres.Options{
		DSN:              cfg.DBDSN.Reveal(),
		MaxConns:         cfg.DBMaxConns,
		ConnectTimeout:   cfg.DBConnectTimeout,
		StatementTimeout: cfg.DBStatementTimeout,
		MaxConnLifetime:  cfg.DBMaxConnLifetime,
		ApplicationName:  "neurodent-api",
		RequireTLS:       cfg.Env != config.EnvDev,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	a.closers = append(a.closers, db)

	a.probe = health.NewProbe(a.log, health.Check{Name: "postgres", Fn: db.Ping})

	mux := http.NewServeMux()
	registerRoutes(mux, a.probe)
	handler := newRouter(mux, a.log, routerOptions{
		handlerTimeout: cfg.HandlerTimeout,
		hsts:           cfg.Env != config.EnvDev,
	})

	a.server = httpserver.New(httpserver.Options{
		Addr:              cfg.HTTPAddr,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ShutdownTimeout:   cfg.ShutdownTimeout,
	}, handler, a.log)

	return a, nil
}

// Run блокируется до отмены ctx (сигнал) или падения сервера и оркестрирует
// остановку: draining → пауза → Shutdown → закрытие ресурсов.
//
// Порядок остановки решает app, а не httpserver: он зависит от нескольких
// компонентов (probe, сервер, позже — NATS-консьюмеры, пул БД). httpserver
// остаётся простым: «работаю до отмены своего ctx».
func (a *App) Run(ctx context.Context) (err error) {
	// Ресурсы закрываются на ЛЮБОМ пути выхода: сигнал, падение сервера, паника.
	defer func() {
		err = errors.Join(err, a.closeAll())
	}()

	// Сервер получает свой контекст, НЕ связанный с отменой ctx. Иначе сигнал
	// сразу остановил бы сервер, и /readyz не успел бы отдать 503.
	// WithoutCancel сохраняет значения ctx (позже — trace-id и т.п.).
	serverCtx, stopServer := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServer()

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.server.Run(serverCtx)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err) // порт занят и т.п.
	case <-ctx.Done():
	}

	a.log.Info("shutting down", "drain_delay", a.cfg.DrainDelay)
	a.probe.StartDraining()

	if d := a.cfg.DrainDelay; d > 0 {
		// select, а не time.Sleep: если сервер упадёт во время паузы, узнаем сразу.
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case err := <-errCh:
			return fmt.Errorf("http server stopped during drain: %w", err)
		}
	}

	stopServer()
	if err := <-errCh; err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	a.log.Info("shutdown complete")
	return nil
}

// closeAll закрывает ресурсы в обратном порядке и собирает все ошибки:
// сбой одного Close не должен помешать закрыть остальные.
func (a *App) closeAll() error {
	var errs []error
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %T: %w", a.closers[i], err))
		}
	}
	a.closers = nil
	return errors.Join(errs...)
}

// newLogger — выбор формата по окружению. Это wiring, поэтому он здесь,
// а не в logger (logger не знает про config) и не в main.
func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	format := logger.FormatJSON
	if cfg.Env == config.EnvDev {
		format = logger.FormatText
	}
	return logger.New(w, cfg.LogLevel, format)
}
