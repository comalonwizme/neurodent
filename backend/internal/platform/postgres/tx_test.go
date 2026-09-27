package postgres // white-box: txState и guardedTx не экспортируются

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

// fakeTx — pgx.Tx, у которого реализованы только методы, нужные guardedTx.
// Остальные методы встроенного nil-интерфейса паникуют, если их вызвать.
type fakeTx struct {
	pgx.Tx
	entered chan<- struct{} // если не nil, Exec сообщает, что начался…
	gate    <-chan struct{} // …и ждёт закрытия gate
}

func (f fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.gate
	}
	return pgconn.CommandTag{}, nil
}

func withFakeTx(ctx context.Context, sc scope.Scope, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, &txState{tx: tx, scope: sc})
}

func staffScope() scope.Scope {
	return scope.Tenant(tenant.FromID(id.New()), scope.Staff(id.New()))
}

func TestWithinTx_RejectsInvalidScopeBeforeTouchingThePool(t *testing.T) {
	db := &DB{} // pool == nil: обращение к нему — паника
	err := db.WithinTx(t.Context(), scope.Scope{}, func(context.Context) error {
		t.Error("fn called with an invalid scope")
		return nil
	})
	if !errors.Is(err, scope.ErrInvalid) {
		t.Errorf("WithinTx(zero scope) = %v, want scope.ErrInvalid", err)
	}
}

func TestWithinTx_ScopeMismatchIsCheckedBeforeSQL(t *testing.T) {
	ctx := withFakeTx(t.Context(), staffScope(), fakeTx{}) // SAVEPOINT на fakeTx паникнул бы
	err := (&DB{}).WithinTx(ctx, staffScope(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrScopeMismatch) {
		t.Errorf("nested other scope = %v, want ErrScopeMismatch", err)
	}
}

func TestWithinStandaloneTx_InsideTx(t *testing.T) {
	sc := staffScope()
	ctx := withFakeTx(t.Context(), sc, fakeTx{})
	if err := (&DB{}).WithinStandaloneTx(ctx, sc, func(context.Context) error { return nil }); !errors.Is(err, ErrInsideTx) {
		t.Errorf("WithinStandaloneTx inside tx = %v, want ErrInsideTx", err)
	}
	if !InTx(ctx) || InTx(t.Context()) {
		t.Error("InTx is wrong")
	}
}

func TestTx_WithoutTransaction(t *testing.T) {
	if _, err := Tx(t.Context()); !errors.Is(err, ErrNoTx) {
		t.Errorf("Tx(no tx) = %v, want ErrNoTx", err)
	}
}

// TestGuardedTx_ConcurrentUse: вторая goroutine, пришедшая, пока первая
// держит соединение, получает ошибку, а не доступ к занятому соединению.
func TestGuardedTx_ConcurrentUse(t *testing.T) {
	entered, gate := make(chan struct{}), make(chan struct{})
	ctx := withFakeTx(t.Context(), staffScope(), fakeTx{entered: entered, gate: gate})
	q, err := Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	var started sync.WaitGroup
	started.Go(func() {
		_, err := q.Exec(ctx, "first")
		first <- err
	})
	// Первая goroutine внутри Exec, значит, соединение уже захвачено.
	<-entered

	// Второй вызов — в своей goroutine со страховкой: без защиты он вошёл бы
	// в занятое соединение и повис, а тест должен упасть, а не зависнуть.
	second := make(chan error, 1)
	go func() {
		_, err := q.Exec(ctx, "second")
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, ErrTxConcurrentUse) {
			t.Errorf("second Exec = %v, want ErrTxConcurrentUse", err)
		}
	case <-entered:
		t.Error("second Exec entered a busy connection: concurrent use was not rejected")
		go func() { <-second }()
	case <-time.After(5 * time.Second):
		t.Fatal("second Exec neither failed nor entered")
	}
	close(gate)
	started.Wait()
	if err := <-first; err != nil {
		t.Errorf("first Exec = %v", err)
	}
	// Третий вызов снова займёт fakeTx: отпускаем его сразу.
	go func() { <-entered }()
	if _, err := q.Exec(ctx, "third"); err != nil {
		t.Errorf("Exec after release = %v", err)
	}
}
