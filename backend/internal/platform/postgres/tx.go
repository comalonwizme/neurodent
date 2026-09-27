package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/txn"
)

// Ошибки транзакций (ADR-0012). Все — ошибки программиста, а не клиента:
// они должны падать громко в тестах, а не превращаться в «ноль строк».
var (
	ErrNoTx            = errors.New("no transaction in context: repositories and contracts run inside WithinTx")
	ErrScopeMismatch   = errors.New("nested transaction requested a different scope")
	ErrInsideTx        = errors.New("must be called outside a transaction: the write must not roll back with it")
	ErrTxConcurrentUse = errors.New("transaction used concurrently: a pgx transaction is a single connection")
)

// Проверка на компиляции: DB — реализация порта транзакций для use cases.
var _ txn.Runner = (*DB)(nil)

// rollbackTimeout — бюджет на откат, когда контекст запроса уже отменён.
const rollbackTimeout = 5 * time.Second

// setScopeSQL выставляет параметры, которые читают RLS-политики и журнал
// аудита (ADR-0011). Третий аргумент set_config = true — это SET LOCAL:
// значение живёт до конца транзакции и не протекает в следующую
// транзакцию на том же соединении пула. Выставляются все четыре, пустая
// строка — «не задано» (функции platform.current_*() превращают её в NULL).
const setScopeSQL = `SELECT
	set_config('app.tenant_id', $1, true),
	set_config('app.actor_kind', $2, true),
	set_config('app.actor_id', $3, true),
	set_config('app.patient_id', $4, true)`

type txKey struct{}

// txState — транзакция в контексте. busy не даёт двум goroutine
// одновременно работать с одним соединением.
type txState struct {
	tx    pgx.Tx
	scope scope.Scope
	busy  atomic.Bool
}

func currentTx(ctx context.Context) (*txState, bool) {
	st, ok := ctx.Value(txKey{}).(*txState)
	return st, ok
}

func (st *txState) acquire() error {
	if !st.busy.CompareAndSwap(false, true) {
		return ErrTxConcurrentUse
	}
	return nil
}

func (st *txState) release() { st.busy.Store(false) }

// InTx сообщает, есть ли в контексте открытая транзакция.
func InTx(ctx context.Context) bool {
	_, ok := currentTx(ctx)
	return ok
}

// WithinTx выполняет fn в транзакции со scope sc: коммит только при
// успехе, откат при ошибке fn, ошибке коммита и панике.
//
// Scope проверяется до BEGIN: нулевой или несовместимый scope — ошибка
// (scope.ErrInvalid). Первым запросом транзакция выставляет параметры
// scope для RLS.
//
// Транзакцию открывает use case. Репозитории и реализации контрактов
// транзакций не открывают, а берут текущую через Tx(ctx).
//
// Если транзакция в контексте уже есть:
//   - тот же scope — fn выполняется в той же транзакции под SAVEPOINT:
//     одно соединение, а ошибка fn откатывает только её работу. Без
//     savepoint любая ошибка SQL переводит всю транзакцию Postgres в
//     aborted, и внешний код уже не смог бы обработать ошибку и продолжить;
//   - другой scope — ErrScopeMismatch: смена клиники или субъекта внутри
//     транзакции — всегда ошибка логики.
//
// Ошибка fn возвращается как есть, без обёртки: это ошибка вызывающего.
func (db *DB) WithinTx(ctx context.Context, sc scope.Scope, fn func(ctx context.Context) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if st, ok := currentTx(ctx); ok {
		if st.scope != sc {
			return fmt.Errorf("%w: current %s, requested %s", ErrScopeMismatch, st.scope, sc)
		}
		return st.withinSavepoint(ctx, fn)
	}
	return db.begin(ctx, sc, fn)
}

// WithinStandaloneTx — WithinTx, который обязан быть самостоятельной
// транзакцией: при открытой транзакции в контексте — ErrInsideTx.
//
// Для записей, которые не должны откатываться вместе с бизнес-транзакцией
// (события отказа в аудите, счётчики лимитов). Присоединиться к текущей
// нельзя — откат унёс бы запись; открыть вторую рядом — значит держать два
// соединения, что при исчерпанном пуле ведёт к взаимоблокировке.
func (db *DB) WithinStandaloneTx(ctx context.Context, sc scope.Scope, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return ErrInsideTx
	}
	return db.WithinTx(ctx, sc, fn)
}

func (db *DB) begin(ctx context.Context, sc scope.Scope, fn func(ctx context.Context) error) (err error) {
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

	tenantID, kind, actorID, patientID := settings(sc)
	if _, err := tx.Exec(ctx, setScopeSQL, tenantID, kind, actorID, patientID); err != nil {
		return fmt.Errorf("set scope: %w", err)
	}

	st := &txState{tx: tx, scope: sc}
	if err := fn(context.WithValue(ctx, txKey{}, st)); err != nil {
		return err
	}
	if err := st.acquire(); err != nil {
		return err
	}
	defer st.release()
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

func (st *txState) withinSavepoint(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if err := st.acquire(); err != nil {
		return err
	}
	sp, err := st.tx.Begin(ctx) // pgx: вложенный Begin = SAVEPOINT
	st.release()
	if err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}

	released := false
	defer func() {
		if released {
			return
		}
		if aErr := st.acquire(); aErr != nil {
			err = errors.Join(err, aErr)
			return
		}
		defer st.release()
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rbErr := sp.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback to savepoint: %w", rbErr))
		}
	}()

	if err := fn(ctx); err != nil {
		return err
	}
	if err := st.acquire(); err != nil {
		return err
	}
	defer st.release()
	if err := sp.Commit(ctx); err != nil { // RELEASE SAVEPOINT
		return fmt.Errorf("release savepoint: %w", err)
	}
	released = true
	return nil
}

func settings(sc scope.Scope) (tenantID, kind, actorID, patientID string) {
	if t, ok := sc.TenantID(); ok {
		tenantID = t.String()
	}
	a := sc.Actor()
	if u, ok := a.UserID(); ok {
		actorID = u.String()
	}
	if p, ok := a.PatientID(); ok {
		patientID = p.String()
	}
	return tenantID, a.Kind().String(), actorID, patientID
}

// DBTX — то, чем адаптеры выполняют запросы в текущей транзакции. Его
// принимает сгенерированный sqlc-код (sqlcdb.New(q)).
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tx возвращает текущую транзакцию из контекста или ErrNoTx.
//
// Транзакция — одно соединение с последовательным протоколом. Возвращаемая
// обёртка держит флаг занятости на время Exec, QueryRow…Scan и Query…Close:
// одновременное использование из двух goroutine вернёт ErrTxConcurrentUse,
// а не перемешает протокол. Goroutine, запущенная внутри транзакции, не
// должна получать её контекст (ADR-0012).
func Tx(ctx context.Context) (DBTX, error) {
	st, ok := currentTx(ctx)
	if !ok {
		return nil, ErrNoTx
	}
	return guardedTx{st: st}, nil
}

type guardedTx struct{ st *txState }

func (g guardedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := g.st.acquire(); err != nil {
		return pgconn.CommandTag{}, err
	}
	defer g.st.release()
	return g.st.tx.Exec(ctx, sql, args...)
}

func (g guardedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := g.st.acquire(); err != nil {
		return nil, err
	}
	rows, err := g.st.tx.Query(ctx, sql, args...)
	if err != nil {
		g.st.release()
		return nil, err
	}
	return &guardedRows{Rows: rows, release: sync.OnceFunc(g.st.release)}, nil
}

func (g guardedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := g.st.acquire(); err != nil {
		return errRow{err: err}
	}
	return guardedRow{row: g.st.tx.QueryRow(ctx, sql, args...), release: g.st.release}
}

// guardedRows освобождает транзакцию, когда строки прочитаны или закрыты.
type guardedRows struct {
	pgx.Rows
	release func()
}

func (r *guardedRows) Next() bool {
	if r.Rows.Next() {
		return true
	}
	r.release() // pgx закрывает Rows, когда строки кончились
	return false
}

func (r *guardedRows) Close() {
	r.Rows.Close()
	r.release()
}

type guardedRow struct {
	row     pgx.Row
	release func()
}

func (r guardedRow) Scan(dest ...any) error {
	defer r.release()
	return r.row.Scan(dest...)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
