// Package httpserver — HTTP-сервер с таймаутами и graceful shutdown.
//
// Сервер знает только «работаю до отмены своего ctx». Порядок остановки
// приложения (drain, пауза, закрытие ресурсов) решает app.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// maxHeaderBytes — лимит на строку запроса и заголовки. Наши клиенты — браузер
// с одной session-cookie и мобильное приложение: им хватает единиц КиБ.
// 32 КиБ оставляют запас на длинные cookie и заголовки трассировки от LB.
// Дефолт stdlib (1 МиБ) защиты не даёт: столько памяти на соединение до
// первого байта хендлера — дешёвая атака. Превышение → 431.
const maxHeaderBytes = 32 << 10

// Options — адрес и таймауты сервера. Значения приходят из config,
// там же обоснованы.
type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Server — http.Server плюс логика остановки.
type Server struct {
	srv             *http.Server
	log             *slog.Logger
	addr            string
	shutdownTimeout time.Duration
}

// New создаёт сервер. Ошибки самого net/http (например, TLS handshake)
// пишутся в log на уровне error.
func New(opts Options, h http.Handler, log *slog.Logger) *Server {
	return &Server{
		srv: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: opts.ReadHeaderTimeout,
			ReadTimeout:       opts.ReadTimeout,
			WriteTimeout:      opts.WriteTimeout,
			IdleTimeout:       opts.IdleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
		},
		log:             log,
		addr:            opts.Addr,
		shutdownTimeout: opts.ShutdownTimeout,
	}
}

// Run слушает Options.Addr и обслуживает запросы до отмены ctx (см. Serve).
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve обслуживает запросы на ln до отмены ctx, затем выполняет graceful
// shutdown: ждёт запросы в полёте не дольше ShutdownTimeout и обрывает
// оставшиеся. Возвращает nil при чистой остановке; ошибку Serve, если
// сервер упал сам; ошибку, оборачивающую context.DeadlineExceeded, если
// пришлось обрывать соединения.
//
// Listener передаётся снаружи, чтобы тесты могли слушать 127.0.0.1:0 и
// знать порт до старта.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.log.Info("http server listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("http server shutting down", "timeout", s.shutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		closeErr := s.srv.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("force close: %w", closeErr)
		}
		return errors.Join(fmt.Errorf("graceful shutdown: %w", err), closeErr)
	}

	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	s.log.Info("http server stopped")
	return nil
}
