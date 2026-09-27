//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

func testTransactions(t *testing.T, env *pgEnv) {
	t.Run("zero scope is rejected before BEGIN", func(t *testing.T) {
		called := false
		err := env.db.WithinTx(t.Context(), scope.Scope{}, func(context.Context) error {
			called = true
			return nil
		})
		if !errors.Is(err, scope.ErrInvalid) || called {
			t.Errorf("WithinTx(zero scope) = %v, fn called = %v; want ErrInvalid and no call", err, called)
		}
	})

	t.Run("scope parameters are set for every actor kind", func(t *testing.T) {
		clinic := newClinic()
		user, patient := id.New(), id.New()
		tests := []struct {
			sc   scope.Scope
			want [4]string // tenant, kind, actor, patient
		}{
			{scope.Tenant(clinic, scope.Staff(user)), [4]string{clinic.String(), "staff", user.String(), ""}},
			{scope.Tenant(clinic, scope.PatientResolve(user)), [4]string{clinic.String(), "patient_resolve", user.String(), ""}},
			{scope.Tenant(clinic, scope.Patient(user, patient)), [4]string{clinic.String(), "patient", user.String(), patient.String()}},
			{scope.Tenant(clinic, scope.System()), [4]string{clinic.String(), "system", "", ""}},
			{scope.Global(scope.User(user)), [4]string{"", "user", user.String(), ""}},
			{scope.Global(scope.Anonymous()), [4]string{"", "anonymous", "", ""}},
			{scope.Global(scope.System()), [4]string{"", "system", "", ""}},
		}
		for _, tt := range tests {
			var got [4]string
			err := inTx(t.Context(), env.db, tt.sc, func(ctx context.Context, q postgres.DBTX) error {
				return q.QueryRow(ctx, `SELECT current_setting('app.tenant_id'), current_setting('app.actor_kind'),
					current_setting('app.actor_id'), current_setting('app.patient_id')`).Scan(&got[0], &got[1], &got[2], &got[3])
			})
			if err != nil {
				t.Fatalf("%s: %v", tt.sc, err)
			}
			if got != tt.want {
				t.Errorf("%s: settings = %q, want %q", tt.sc, got, tt.want)
			}
		}
	})

	t.Run("nested call with the same scope joins one transaction on one connection", func(t *testing.T) {
		// MaxConns=1: если бы вложенный вызов брал второе соединение, он бы
		// ждал его вечно. Таймаут — страховка, а не синхронизация.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		db := appDB(t, env, 1)
		sc := asStaff(newClinic())

		const ident = "SELECT txid_current()::text || '/' || pg_backend_pid()::text"
		var outer, inner string
		err := inTx(ctx, db, sc, func(ctx context.Context, q postgres.DBTX) error {
			if err := q.QueryRow(ctx, ident).Scan(&outer); err != nil {
				return err
			}
			return inTx(ctx, db, sc, func(ctx context.Context, q postgres.DBTX) error {
				return q.QueryRow(ctx, ident).Scan(&inner)
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if outer == "" || outer != inner {
			t.Errorf("outer tx/pid = %s, nested = %s: want the same transaction and connection", outer, inner)
		}
	})

	t.Run("nested call with a different scope is rejected", func(t *testing.T) {
		clinic := newClinic()
		user := id.New()
		outer := scope.Tenant(clinic, scope.Staff(user))
		others := []scope.Scope{
			scope.Tenant(newClinic(), scope.Staff(user)),
			scope.Tenant(clinic, scope.Staff(id.New())),
			scope.Tenant(clinic, scope.System()),
			scope.Global(scope.User(user)),
		}
		for _, other := range others {
			var nestedErr error
			err := inTx(t.Context(), env.db, outer, func(ctx context.Context, q postgres.DBTX) error {
				nestedErr = env.db.WithinTx(ctx, other, func(context.Context) error { return nil })
				// Ошибка scope не трогает транзакцию: внешний код продолжает.
				_, err := q.Exec(ctx, "SELECT 1")
				return err
			})
			if err != nil {
				t.Errorf("outer transaction broken after mismatch: %v", err)
			}
			if !errors.Is(nestedErr, postgres.ErrScopeMismatch) {
				t.Errorf("nested %s inside %s: %v, want ErrScopeMismatch", other, outer, nestedErr)
			}
		}
	})

	t.Run("nested SQL error rolls back only its savepoint", func(t *testing.T) {
		ctx := t.Context()
		clinic := newClinic()
		sc := asStaff(clinic)
		dup := id.New()
		insert := func(q postgres.DBTX, ctx context.Context, rowID id.ID, note string) error {
			_, err := q.Exec(ctx, "INSERT INTO platform.rls_probe (id, tenant_id, note) VALUES ($1, $2, $3)", rowID.String(), clinic.String(), note)
			return err
		}

		var nestedErr error
		err := inTx(ctx, env.db, sc, func(ctx context.Context, q postgres.DBTX) error {
			if err := insert(q, ctx, dup, "outer-before"); err != nil {
				return err
			}
			nestedErr = inTx(ctx, env.db, sc, func(ctx context.Context, q postgres.DBTX) error {
				if err := insert(q, ctx, id.New(), "nested"); err != nil {
					return err
				}
				return insert(q, ctx, dup, "nested-duplicate") // unique_violation
			})
			// Без savepoint здесь была бы ошибка 25P02: транзакция в aborted.
			return insert(q, ctx, id.New(), "outer-after")
		})
		if err != nil {
			t.Fatalf("outer transaction: %v", err)
		}
		if pgCode(nestedErr) != codeUniqueViolation {
			t.Errorf("nested error = %v, want unique_violation", nestedErr)
		}
		got := strings.Join(strings1(t, ctx, env.db, sc, "SELECT note FROM platform.rls_probe ORDER BY note"), ",")
		if got != "outer-after,outer-before" {
			t.Errorf("rows = %s, want only the outer ones", got)
		}
	})

	t.Run("commit on success, rollback on error and panic", func(t *testing.T) {
		ctx := t.Context()
		clinic := newClinic()
		sc := asStaff(clinic)
		errBoom := errors.New("boom")
		insert := func(ctx context.Context, note string) error {
			q, err := postgres.Tx(ctx)
			if err != nil {
				return err
			}
			_, err = q.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, $2)", clinic.String(), note)
			return err
		}

		if err := env.db.WithinTx(ctx, sc, func(ctx context.Context) error { return insert(ctx, "committed") }); err != nil {
			t.Fatal(err)
		}
		err := env.db.WithinTx(ctx, sc, func(ctx context.Context) error {
			if err := insert(ctx, "rolled back on error"); err != nil {
				return err
			}
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Errorf("WithinTx() = %v, want the fn error unchanged", err)
		}
		func() {
			defer func() {
				if p := recover(); p != "db panic" {
					t.Errorf("panic = %v, want it to propagate", p)
				}
			}()
			_ = env.db.WithinTx(ctx, sc, func(ctx context.Context) error {
				if err := insert(ctx, "rolled back on panic"); err != nil {
					return err
				}
				panic("db panic")
			})
		}()

		if got := strings.Join(strings1(t, ctx, env.db, sc, "SELECT note FROM platform.rls_probe"), ","); got != "committed" {
			t.Errorf("rows = %s, want only [committed]", got)
		}
	})

	t.Run("concurrent use of the transaction fails loudly", func(t *testing.T) {
		err := inTx(t.Context(), env.db, asStaff(newClinic()), func(ctx context.Context, q postgres.DBTX) error {
			// Детерминированно: пока строки не закрыты, соединение занято.
			rows, err := q.Query(ctx, "SELECT generate_series(1, 3)")
			if err != nil {
				return err
			}
			if _, err := q.Exec(ctx, "SELECT 1"); !errors.Is(err, postgres.ErrTxConcurrentUse) {
				t.Errorf("Exec while rows are open = %v, want ErrTxConcurrentUse", err)
			}
			var n int
			if err := q.QueryRow(ctx, "SELECT 1").Scan(&n); !errors.Is(err, postgres.ErrTxConcurrentUse) {
				t.Errorf("QueryRow while rows are open = %v, want ErrTxConcurrentUse", err)
			}
			rows.Close()
			if _, err := q.Exec(ctx, "SELECT 1"); err != nil {
				t.Errorf("Exec after rows.Close = %v", err)
			}

			// Реальные goroutine: каждая либо выполняет запрос, либо получает
			// ErrTxConcurrentUse — но никогда не ломает протокол соединения.
			var wg sync.WaitGroup
			errs := make([]error, 16)
			for i := range errs {
				wg.Go(func() {
					var v int
					errs[i] = q.QueryRow(ctx, "SELECT 1").Scan(&v)
				})
			}
			wg.Wait()
			for _, err := range errs {
				if err != nil && !errors.Is(err, postgres.ErrTxConcurrentUse) {
					t.Errorf("concurrent query failed with %v, want nil or ErrTxConcurrentUse", err)
				}
			}
			_, err = q.Exec(ctx, "SELECT 1")
			return err
		})
		if err != nil {
			t.Errorf("transaction unusable after concurrent attempts: %v", err)
		}
	})

	t.Run("standalone transaction inside a transaction is rejected", func(t *testing.T) {
		sc := scope.Global(scope.System())
		var inner error
		err := env.db.WithinTx(t.Context(), sc, func(ctx context.Context) error {
			inner = env.db.WithinStandaloneTx(ctx, sc, func(context.Context) error { return nil })
			return nil
		})
		if err != nil || !errors.Is(inner, postgres.ErrInsideTx) {
			t.Errorf("WithinStandaloneTx inside tx = %v (outer %v), want ErrInsideTx", inner, err)
		}
		if err := env.db.WithinStandaloneTx(t.Context(), sc, func(context.Context) error { return nil }); err != nil {
			t.Errorf("WithinStandaloneTx outside tx = %v", err)
		}
	})

	t.Run("transaction is closed after WithinTx returns", func(t *testing.T) {
		var leaked context.Context
		if err := env.db.WithinTx(t.Context(), asStaff(newClinic()), func(ctx context.Context) error {
			leaked = ctx
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		q, err := postgres.Tx(leaked)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(t.Context(), "SELECT 1"); !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("use after commit = %v, want pgx.ErrTxClosed", err)
		}
	})
}
