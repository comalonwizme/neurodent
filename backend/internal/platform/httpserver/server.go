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

const maxHeaderBytes = 1 << 32

type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type Server struct {
	srv             *http.Server
	log             *slog.Logger
	addr            string
	shutdownTimeout time.Duration
}

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

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	return s.Serve(ctx, ln)
}
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
