// Package txn — порт транзакций для use cases (ADR-0012). Use case открывает
// транзакцию через Runner, не зная о Postgres: платформенную реализацию
// (platform/postgres.DB) подставляет internal/app.
package txn

import (
	"context"

	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

// Runner выполняет fn в транзакции со scope sc: коммит только при успехе,
// откат при ошибке и панике. Вложенный вызов с тем же scope присоединяется
// к текущей транзакции, с другим — ошибка (подробности — у реализации).
type Runner interface {
	WithinTx(ctx context.Context, sc scope.Scope, fn func(ctx context.Context) error) error
}
