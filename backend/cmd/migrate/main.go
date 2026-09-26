// Команда migrate применяет встроенные SQL-миграции от имени владельца
// схемы (NEURODENT_MIGRATE_DSN). API миграции не запускает: у его роли нет
// прав на DDL, и так остаётся.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/config"
	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/migrations"
)

// connectTimeout — мигратор запускается разово (job при деплое, make
// migrate); 10s хватает на холодный старт БД в docker-compose.
const connectTimeout = 10 * time.Second

// main отвечает только за код возврата; os.Exit — только здесь.
func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadMigrate()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	format := logger.FormatJSON
	if cfg.Env == config.EnvDev {
		format = logger.FormatText
	}
	log := logger.New(os.Stdout, slog.LevelInfo, format)

	conn, err := postgres.Connect(ctx, cfg.DSN.Reveal(), cfg.Env != config.EnvDev, connectTimeout, "neurodent-migrate")
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(ctx)); err != nil {
			log.Warn("close connection failed", "err", err)
		}
	}()

	if err := postgres.Migrate(ctx, conn, migrations.FS(), log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
