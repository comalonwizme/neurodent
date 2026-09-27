//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

func insertProbe(t *testing.T, ctx context.Context, db *postgres.DB, clinic tenant.ID, note string) {
	t.Helper()
	err := inTx(ctx, db, asStaff(clinic), func(ctx context.Context, q postgres.DBTX) error {
		_, err := q.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, $2)", clinic.String(), note)
		return err
	})
	if err != nil {
		t.Fatalf("insert probe: %v", err)
	}
}

func testRLS(t *testing.T, env *pgEnv) {
	t.Run("clinic sees only its own rows", func(t *testing.T) {
		ctx := t.Context()
		a, b := newClinic(), newClinic()
		for _, c := range []tenant.ID{a, a, b} {
			insertProbe(t, ctx, env.db, c, "isolation")
		}
		for _, c := range []tenant.ID{a, b} {
			got := strings1(t, ctx, env.db, asStaff(c), "SELECT tenant_id::text FROM platform.rls_probe")
			if len(got) == 0 {
				t.Errorf("clinic %s sees none of its rows", c)
			}
			for _, tid := range got {
				if tid != c.String() {
					t.Errorf("clinic %s sees a row of clinic %s", c, tid)
				}
			}
		}
	})

	t.Run("global scope sees zero tenant rows even on a reused connection", func(t *testing.T) {
		ctx := t.Context()
		// Одно соединение: вторая транзакция гарантированно идёт по тому же
		// соединению, где до этого был SET LOCAL с клиникой.
		db := appDB(t, env, 1)
		insertProbe(t, ctx, db, newClinic(), "reuse")
		sc := scope.Global(scope.System())
		if got := strings1(t, ctx, db, sc, "SELECT current_setting('app.tenant_id')"); len(got) != 1 || got[0] != "" {
			t.Errorf("app.tenant_id in global scope = %q, want ''", got)
		}
		if got := strings1(t, ctx, db, sc, "SELECT note FROM platform.rls_probe"); len(got) != 0 {
			t.Errorf("global transaction sees %d tenant rows, want 0", len(got))
		}
	})

	t.Run("cross-clinic insert is rejected by WITH CHECK", func(t *testing.T) {
		a, b := newClinic(), newClinic()
		err := exec(t.Context(), env.db, asStaff(a),
			"INSERT INTO platform.rls_probe (tenant_id, note) VALUES ('"+b.String()+"', 'x')")
		if pgCode(err) != codeInsufficientPrivilege {
			t.Errorf("insert for another clinic: err = %v, want SQLSTATE 42501", err)
		}
	})

	t.Run("app role cannot bypass RLS", func(t *testing.T) {
		ctx := t.Context()
		clinic := newClinic()
		insertProbe(t, ctx, env.db, clinic, "bypass")
		attempts := []struct {
			name string
			sql  []string
		}{
			{"disable RLS", []string{"ALTER TABLE platform.rls_probe DISABLE ROW LEVEL SECURITY"}},
			{"drop FORCE", []string{"ALTER TABLE platform.rls_probe NO FORCE ROW LEVEL SECURITY"}},
			{"drop policy", []string{"DROP POLICY tenant_guard ON platform.rls_probe"}},
			{"add permissive policy", []string{"CREATE POLICY open ON platform.rls_probe USING (true)"}},
			{"row_security off", []string{"SET LOCAL row_security = off", "SELECT count(*) FROM platform.rls_probe"}},
			{"become owner", []string{"SET LOCAL ROLE neurodent_owner"}},
		}
		for _, a := range attempts {
			if err := exec(ctx, env.db, asStaff(clinic), a.sql...); pgCode(err) != codeInsufficientPrivilege {
				t.Errorf("%s: err = %v, want SQLSTATE 42501", a.name, err)
			}
		}

		got := strings1(t, ctx, env.db, scope.Global(scope.System()), `
			SELECT r.rolsuper::text || ',' || r.rolbypassrls::text || ',' || t.tableowner
			FROM pg_roles r, pg_tables t
			WHERE r.rolname = current_user AND t.schemaname = 'platform' AND t.tablename = 'rls_probe'`)
		if strings.Join(got, "") != "false,false,neurodent_owner" {
			t.Errorf("app role superuser,bypassrls,table owner = %v", got)
		}
	})

	t.Run("FORCE RLS restricts the owner too", func(t *testing.T) {
		ctx := t.Context()
		insertProbe(t, ctx, env.db, newClinic(), "force")
		var n int
		if err := ownerConn(t, env).QueryRow(ctx, "SELECT count(*) FROM platform.rls_probe").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("owner without clinic sees %d rows, want 0 (FORCE ROW LEVEL SECURITY)", n)
		}
	})
}

func testPool(t *testing.T, env *pgEnv) {
	sc := scope.Global(scope.System())

	t.Run("session settings come from options", func(t *testing.T) {
		got := strings1(t, t.Context(), env.db, sc, "SELECT current_setting('statement_timeout') || ',' || current_setting('application_name')")
		if strings.Join(got, "") != "1500ms,it-app" {
			t.Errorf("statement_timeout,application_name = %v", got)
		}
	})

	t.Run("statement timeout cancels long queries", func(t *testing.T) {
		if err := exec(t.Context(), env.db, sc, "SELECT pg_sleep(5)"); pgCode(err) != codeQueryCanceled {
			t.Errorf("pg_sleep(5) with 1.5s statement_timeout: err = %v, want SQLSTATE 57014", err)
		}
	})

	t.Run("readiness pings the database", func(t *testing.T) {
		db := appDB(t, env, 1)
		probe := health.NewProbe(env.log, health.Check{Name: "postgres", Fn: db.Ping})

		rec := httptest.NewRecorder()
		probe.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("readyz with live db = %d, want 200", rec.Code)
		}

		_ = db.Close()
		if err := db.WithinTx(t.Context(), sc, func(context.Context) error { return nil }); err == nil || !strings.HasPrefix(err.Error(), "begin tx: ") {
			t.Errorf("WithinTx on closed pool = %v, want begin error", err)
		}
		rec = httptest.NewRecorder()
		probe.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("readyz with closed pool = %d, want 503", rec.Code)
		}
	})
}
