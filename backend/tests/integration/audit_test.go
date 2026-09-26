//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/comalonwizme/neurodent/backend/internal/platform/audit"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

type storedEvent struct {
	tenant, actorKind, actorID, action, resourceID, outcome, requestID string
	occurredAt, recordedAt                                             time.Time
}

// eventsOf читает события ресурса владельцем схемы. Под FORCE RLS владельцу
// тоже нужна клиника: политика audit_read отдаёт только её события
// (tenant нулевой — события вне клиники не читаются вовсе).
func eventsOf(t *testing.T, env *pgEnv, clinic tenant.ID, resource id.ID) []storedEvent {
	t.Helper()
	ctx := t.Context()
	var out []storedEvent
	err := pgx.BeginFunc(ctx, ownerConn(t, env), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", clinic.String()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT coalesce(tenant_id::text, ''), actor_kind, coalesce(actor_id::text, ''), action,
			coalesce(resource_id::text, ''), outcome, coalesce(request_id, ''), occurred_at, recorded_at
			FROM audit.events WHERE resource_id = $1 ORDER BY recorded_at`, resource.String())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedEvent, error) {
			var e storedEvent
			err := r.Scan(&e.tenant, &e.actorKind, &e.actorID, &e.action, &e.resourceID, &e.outcome, &e.requestID, &e.occurredAt, &e.recordedAt)
			return e, err
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testAudit(t *testing.T, env *pgEnv) {
	ctx := t.Context()
	fixed := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	rec := audit.NewRecorder(env.db, clock.NewManual(fixed))
	ev := func(resource id.ID) audit.Event {
		return audit.Event{Action: "platform.probe.written", ResourceType: "platform.probe", ResourceID: resource, Outcome: audit.Success}
	}

	t.Run("app role can only insert", func(t *testing.T) {
		clinic := newClinic()
		sc := asStaff(clinic)
		for _, q := range []string{
			"SELECT count(*) FROM audit.events",
			"UPDATE audit.events SET outcome = 'success'",
			"DELETE FROM audit.events",
			"TRUNCATE audit.events",
		} {
			if err := exec(ctx, env.db, sc, q); pgCode(err) != codeInsufficientPrivilege {
				t.Errorf("%s: err = %v, want 42501", q, err)
			}
		}
	})

	t.Run("event fields come from scope, clock and request", func(t *testing.T) {
		clinic, user, resource := newClinic(), id.New(), id.New()
		rctx := httpx.WithRequestID(ctx, "req-audit-1")
		err := env.db.WithinTx(rctx, scope.Tenant(clinic, scope.Staff(user)), func(ctx context.Context) error {
			return rec.Record(ctx, ev(resource))
		})
		if err != nil {
			t.Fatal(err)
		}
		got := eventsOf(t, env, clinic, resource)
		if len(got) != 1 {
			t.Fatalf("events = %+v, want one", got)
		}
		e := got[0]
		if e.tenant != clinic.String() || e.actorKind != "staff" || e.actorID != user.String() ||
			e.action != "platform.probe.written" || e.outcome != "success" || e.requestID != "req-audit-1" {
			t.Errorf("event = %+v", e)
		}
		if !e.occurredAt.Equal(fixed) {
			t.Errorf("occurred_at = %v, want the injected clock %v", e.occurredAt, fixed)
		}
	})

	t.Run("event rolls back with the action", func(t *testing.T) {
		clinic, resource := newClinic(), id.New()
		errBoom := errors.New("boom")
		err := env.db.WithinTx(ctx, asStaff(clinic), func(ctx context.Context) error {
			if err := rec.Record(ctx, ev(resource)); err != nil {
				return err
			}
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if got := eventsOf(t, env, clinic, resource); len(got) != 0 {
			t.Errorf("event of a rolled back action survived: %+v", got)
		}
	})

	t.Run("Do: success in the same transaction, denial and failure after rollback", func(t *testing.T) {
		clinic := newClinic()
		sc := asStaff(clinic)
		write := func(ctx context.Context, note string) error {
			q, err := postgres.Tx(ctx)
			if err != nil {
				return err
			}
			_, err = q.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, $2)", clinic.String(), note)
			return err
		}
		cases := []struct {
			note    string
			fnErr   error
			outcome string
		}{
			{"ok", nil, "success"},
			{"denied", apperr.New(apperr.Forbidden, "not your clinic"), "denied"},
			{"failed", errors.New("storage error"), "failed"},
		}
		for _, c := range cases {
			resource := id.New()
			err := rec.Do(ctx, sc, ev(resource), func(ctx context.Context) error {
				if err := write(ctx, c.note); err != nil {
					return err
				}
				return c.fnErr
			})
			if !errors.Is(err, c.fnErr) {
				t.Errorf("%s: Do() = %v, want the fn error", c.note, err)
			}
			got := eventsOf(t, env, clinic, resource)
			if len(got) != 1 || got[0].outcome != c.outcome {
				t.Errorf("%s: events = %+v, want one %s", c.note, got, c.outcome)
			}
		}
		// Данные неудачных действий откатились, событие о них осталось.
		notes := strings1(t, ctx, env.db, sc, "SELECT note FROM platform.rls_probe")
		if !equalSet(notes, "ok") {
			t.Errorf("rows = %v, want only the successful action", notes)
		}
	})

	t.Run("outcome writers refuse to run inside a transaction", func(t *testing.T) {
		sc := asStaff(newClinic())
		var outcomeErr, doErr error
		err := env.db.WithinTx(ctx, sc, func(ctx context.Context) error {
			outcomeErr = rec.RecordOutcome(ctx, sc, ev(id.New()))
			doErr = rec.Do(ctx, sc, ev(id.New()), func(context.Context) error { return nil })
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(outcomeErr, postgres.ErrInsideTx) || !errors.Is(doErr, postgres.ErrInsideTx) {
			t.Errorf("RecordOutcome = %v, Do = %v; want ErrInsideTx", outcomeErr, doErr)
		}
	})

	t.Run("WITH CHECK keeps events consistent with the transaction scope", func(t *testing.T) {
		clinic, other, user := newClinic(), newClinic(), id.New()
		sc := scope.Tenant(clinic, scope.Staff(user))
		insert := func(tenantSQL, kind, actorSQL string) error {
			return exec(ctx, env.db, sc, `INSERT INTO audit.events (occurred_at, tenant_id, actor_kind, actor_id, action, resource_type, outcome)
				VALUES (now(), `+tenantSQL+`, '`+kind+`', `+actorSQL+`, 'platform.probe.written', 'platform.probe', 'success')`)
		}
		self := "'" + user.String() + "'"
		if err := insert("'"+clinic.String()+"'", "staff", self); err != nil {
			t.Errorf("consistent event rejected: %v", err)
		}
		forged := map[string]error{
			"other clinic":     insert("'"+other.String()+"'", "staff", self),
			"no clinic":        insert("NULL", "staff", self),
			"other actor":      insert("'"+clinic.String()+"'", "staff", "'"+id.New().String()+"'"),
			"other actor kind": insert("'"+clinic.String()+"'", "system", self),
			"no actor":         insert("'"+clinic.String()+"'", "staff", "NULL"),
		}
		for name, err := range forged {
			if pgCode(err) != codeInsufficientPrivilege {
				t.Errorf("%s: err = %v, want 42501", name, err)
			}
		}
		// Событие вне клиники: оба tenant NULL — IS NOT DISTINCT FROM пропускает.
		resource := id.New()
		if err := env.db.WithinTx(ctx, scope.Global(scope.Anonymous()), func(ctx context.Context) error {
			return rec.Record(ctx, audit.Event{Action: "iam.login.attempted", ResourceType: "iam.session", ResourceID: resource, Outcome: audit.Denied})
		}); err != nil {
			t.Errorf("global anonymous event rejected: %v", err)
		}
	})

	t.Run("CHECK constraints reject free text", func(t *testing.T) {
		user := id.New()
		sc := scope.Tenant(newClinic(), scope.Staff(user))
		err := exec(ctx, env.db, sc, `INSERT INTO audit.events (occurred_at, tenant_id, actor_kind, actor_id, action, resource_type, outcome)
			VALUES (now(), platform.current_tenant_id(), 'staff', '`+user.String()+`', 'patient Ivanov viewed', 'platform.probe', 'success')`)
		if pgCode(err) != codeCheckViolation {
			t.Errorf("free-text action: %v, want 23514", err)
		}
	})

	t.Run("recorded_at is the insert time, not the transaction start", func(t *testing.T) {
		clinic := newClinic()
		first, second := id.New(), id.New()
		err := env.db.WithinTx(ctx, asStaff(clinic), func(ctx context.Context) error {
			if err := rec.Record(ctx, ev(first)); err != nil {
				return err
			}
			q, err := postgres.Tx(ctx)
			if err != nil {
				return err
			}
			if _, err := q.Exec(ctx, "SELECT pg_sleep(0.05)"); err != nil { // задержка на стороне БД
				return err
			}
			return rec.Record(ctx, ev(second))
		})
		if err != nil {
			t.Fatal(err)
		}
		a, b := eventsOf(t, env, clinic, first), eventsOf(t, env, clinic, second)
		if len(a) != 1 || len(b) != 1 || !b[0].recordedAt.After(a[0].recordedAt) {
			t.Errorf("recorded_at: %v then %v; want the second later (clock_timestamp, not now())", a, b)
		}
	})

	t.Run("reading is limited to the clinic", func(t *testing.T) {
		mine, theirs, resource := newClinic(), newClinic(), id.New()
		for _, c := range []tenant.ID{mine, theirs} {
			if err := env.db.WithinTx(ctx, asStaff(c), func(ctx context.Context) error {
				return rec.Record(ctx, ev(resource))
			}); err != nil {
				t.Fatal(err)
			}
		}
		got := eventsOf(t, env, mine, resource)
		if len(got) != 1 || got[0].tenant != mine.String() {
			t.Errorf("clinic reads %+v, want only its own event", got)
		}
	})
}
