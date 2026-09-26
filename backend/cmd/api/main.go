// Команда api — HTTP API NeuroDent: читает конфиг из окружения, собирает
// приложение (internal/app) и работает до SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/comalonwizme/neurodent/backend/internal/app"
	"github.com/comalonwizme/neurodent/backend/internal/config"
)

// main отвечает только за код возврата. os.Exit — только здесь: он не
// выполняет defer, поэтому вызывается, когда в стеке их уже не осталось.
func main() {
	if err := run(); err != nil {
		// Конфиг мог не загрузиться, и формат логов неизвестен. Фатальная
		// ошибка всегда пишется JSON в stderr: в проде её разберёт сборщик,
		// в dev она читается и так.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// ctx отменяется при SIGINT (Ctrl+C) или SIGTERM (docker/k8s).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Сразу после первого сигнала снимаем перехват: второй Ctrl+C убьёт
	// процесс немедленно, если graceful shutdown завис.
	context.AfterFunc(ctx, stop)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	a, err := app.New(ctx, cfg, os.Stdout)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}

	return a.Run(ctx)
}
