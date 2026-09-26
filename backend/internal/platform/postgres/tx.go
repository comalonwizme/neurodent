package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

// tenantSetting — имя GUC, которое читают RLS-политики. Должно совпадать
// с миграциями: current_setting('app.tenant_id', true).
const tenantSetting = "app.tenant_id"

// rollbackTimeout — бюджет на откат, когда контекст запроса уже отменён.
const rollbackTimeout = 5 * time.Second

// TxFunc — работа внутри транзакции. ctx тот же, что передан в WithinTx.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// WithinTx выполняет fn в транзакции: коммит только при успехе, откат при
// ошибке fn, ошибке коммита и панике.
//
// Если в ctx есть tenant (tenant.WithID), первым запросом транзакции
// выполняется set_config('app.tenant_id', id, true). Третий аргумент true —
// это SET LOCAL: значение живёт до конца транзакции и не протекает в
// следующую транзакцию на том же соединении пула. Без tenant RLS-политики
// видят NULL и не отдают ни одной строки tenant-таблиц (fail closed).
//
// Ошибка fn возвращается как есть, без обёртки: это ошибка вызывающего,
// с его контекстом и категорией apperr.
//
// Вложенный вызов WithinTx на том же DB возьмёт второе соединение из пула:
// это другая транзакция, и при MaxConns соединений — риск взаимоблокировки.
// Для вложенности используйте tx.Begin (savepoint) внутри fn.
func (db *DB) WithinTx(ctx context.Context, fn TxFunc) (err error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Откат на любом пути, кроме успешного коммита, включая панику: defer
	// выполняется при раскрутке стека, панику мы не перехватываем — она
	// дойдёт до Recover-middleware. После неудачного Commit транзакция уже
	// закрыта, и Rollback вернёт ErrTxClosed — это не ошибка.
	committed := false
	defer func() {
		if committed {
			return
		}
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
	}()

	if id, ok := tenant.FromContext(ctx); ok {
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", tenantSetting, id.String()); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}
