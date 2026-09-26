//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

// clinicFixture — клиника с пациентами для проверки классов таблиц:
//
//	u1 — пациент с аккаунтом (карточка p1) и опекун ребёнка w (карточка без аккаунта);
//	u2 — другой пациент (карточка p2);
//	у каждого пациента по одной медзаписи; в клинике одна публичная строка.
type clinicFixture struct {
	clinic tenant.ID
	p1, p2 id.ID // карточки u1 и u2
	w      id.ID // карточка подопечного u1
}

func seedClinic(t *testing.T, ctx context.Context, db *postgres.DB, u1, u2 id.ID) clinicFixture {
	t.Helper()
	f := clinicFixture{clinic: newClinic(), p1: id.New(), p2: id.New(), w: id.New()}
	c := f.clinic.String()
	err := inTx(ctx, db, asStaff(f.clinic), func(ctx context.Context, q postgres.DBTX) error {
		stmts := []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, 'internal')", []any{c}},
			{"INSERT INTO platform.rls_probe_public (tenant_id, note) VALUES ($1, 'price list')", []any{c}},
			{"INSERT INTO platform.rls_probe_patients (id, tenant_id, user_id, note) VALUES ($1, $2, $3, 'p1')", []any{f.p1.String(), c, u1.String()}},
			{"INSERT INTO platform.rls_probe_patients (id, tenant_id, user_id, note) VALUES ($1, $2, $3, 'p2')", []any{f.p2.String(), c, u2.String()}},
			{"INSERT INTO platform.rls_probe_patients (id, tenant_id, user_id, note) VALUES ($1, $2, NULL, 'w')", []any{f.w.String(), c}},
			{"INSERT INTO platform.rls_probe_guardians (tenant_id, guardian_user_id, ward_patient_id) VALUES ($1, $2, $3)", []any{c, u1.String(), f.w.String()}},
			{"INSERT INTO platform.rls_probe_records (tenant_id, patient_id, note) VALUES ($1, $2, 'record p1')", []any{c, f.p1.String()}},
			{"INSERT INTO platform.rls_probe_records (tenant_id, patient_id, note) VALUES ($1, $2, 'record p2')", []any{c, f.p2.String()}},
			{"INSERT INTO platform.rls_probe_records (tenant_id, patient_id, note) VALUES ($1, $2, 'record w')", []any{c, f.w.String()}},
		}
		for _, s := range stmts {
			if _, err := q.Exec(ctx, s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed clinic: %v", err)
	}
	return f
}

func sorted(s []string) []string { return slices.Sorted(slices.Values(s)) }

func equalSet(got []string, want ...string) bool {
	return slices.Equal(sorted(got), sorted(want))
}

func testClasses(t *testing.T, env *pgEnv) {
	ctx := t.Context()
	u1, u2, stranger := id.New(), id.New(), id.New()
	a := seedClinic(t, ctx, env.db, u1, u2)
	b := seedClinic(t, ctx, env.db, u1, id.New()) // u1 — пациент и клиники b

	patient := func(f clinicFixture, user, card id.ID) scope.Scope {
		return scope.Tenant(f.clinic, scope.Patient(user, card))
	}
	resolve := func(f clinicFixture, user id.ID) scope.Scope {
		return scope.Tenant(f.clinic, scope.PatientResolve(user))
	}
	notes := func(sc scope.Scope, table string) []string {
		return strings1(t, ctx, env.db, sc, "SELECT note FROM platform."+table)
	}

	t.Run("tenant: staff and system only", func(t *testing.T) {
		if got := notes(asStaff(a.clinic), "rls_probe"); !equalSet(got, "internal") {
			t.Errorf("staff sees %v", got)
		}
		if got := notes(scope.Tenant(a.clinic, scope.System()), "rls_probe"); !equalSet(got, "internal") {
			t.Errorf("system sees %v", got)
		}
		for name, sc := range map[string]scope.Scope{
			"patient":         patient(a, u1, a.p1),
			"patient_resolve": resolve(a, u1),
		} {
			if got := notes(sc, "rls_probe"); len(got) != 0 {
				t.Errorf("%s sees tenant rows %v, want none", name, got)
			}
		}
	})

	t.Run("tenant-public: patient reads, cannot write", func(t *testing.T) {
		if got := notes(patient(a, u1, a.p1), "rls_probe_public"); !equalSet(got, "price list") {
			t.Errorf("patient sees %v, want own clinic's public rows", got)
		}
		if got := notes(resolve(a, u1), "rls_probe_public"); len(got) != 0 {
			t.Errorf("patient_resolve sees public rows %v, want none", got)
		}
		err := exec(ctx, env.db, patient(a, u1, a.p1),
			"INSERT INTO platform.rls_probe_public (tenant_id, note) VALUES ('"+a.clinic.String()+"', 'forged')")
		if pgCode(err) != codeInsufficientPrivilege {
			t.Errorf("patient insert into tenant-public: %v, want 42501", err)
		}
	})

	t.Run("patient-registry: resolve sees own and ward cards only", func(t *testing.T) {
		if got := notes(resolve(a, u1), "rls_probe_patients"); !equalSet(got, "p1", "w") {
			t.Errorf("u1 resolves %v, want [p1 w]", got)
		}
		if got := notes(resolve(a, u2), "rls_probe_patients"); !equalSet(got, "p2") {
			t.Errorf("u2 resolves %v, want [p2]", got)
		}
		if got := notes(resolve(a, stranger), "rls_probe_patients"); len(got) != 0 {
			t.Errorf("stranger resolves %v, want none", got)
		}
		if got := notes(resolve(b, u1), "rls_probe_patients"); !equalSet(got, "p1", "w") {
			t.Errorf("u1 in clinic b resolves %v, want b's own cards", got)
		}
		if got := notes(patient(a, u1, a.p1), "rls_probe_patients"); !equalSet(got, "p1") {
			t.Errorf("patient p1 sees cards %v, want only its own", got)
		}
		err := exec(ctx, env.db, resolve(a, u1),
			"INSERT INTO platform.rls_probe_patients (tenant_id, user_id, note) VALUES ('"+a.clinic.String()+"', '"+u1.String()+"', 'self-made')")
		if pgCode(err) != codeInsufficientPrivilege {
			t.Errorf("patient_resolve creates a card: %v, want 42501", err)
		}
	})

	t.Run("patient-owned: patient sees only the chosen card", func(t *testing.T) {
		if got := notes(patient(a, u1, a.p1), "rls_probe_records"); !equalSet(got, "record p1") {
			t.Errorf("u1 as p1 sees %v", got)
		}
		if got := notes(patient(a, u1, a.w), "rls_probe_records"); !equalSet(got, "record w") {
			t.Errorf("u1 as guardian of w sees %v", got)
		}
		if got := notes(patient(a, u2, a.p2), "rls_probe_records"); !equalSet(got, "record p2") {
			t.Errorf("u2 sees %v", got)
		}
		if got := notes(resolve(a, u1), "rls_probe_records"); len(got) != 0 {
			t.Errorf("patient_resolve sees records %v, want none", got)
		}
	})

	t.Run("patient-owned: staff sees every patient of its clinic only", func(t *testing.T) {
		if got := notes(asStaff(a.clinic), "rls_probe_records"); !equalSet(got, "record p1", "record p2", "record w") {
			t.Errorf("staff of a sees %v", got)
		}
		if got := notes(asStaff(newClinic()), "rls_probe_records"); len(got) != 0 {
			t.Errorf("staff of another clinic sees %v", got)
		}
	})

	t.Run("patient-owned: patient cannot write for another patient", func(t *testing.T) {
		c := a.clinic.String()
		if err := exec(ctx, env.db, patient(a, u1, a.p1),
			"INSERT INTO platform.rls_probe_records (tenant_id, patient_id, note) VALUES ('"+c+"', '"+a.p1.String()+"', 'own')"); err != nil {
			t.Errorf("patient writes own record: %v", err)
		}
		err := exec(ctx, env.db, patient(a, u1, a.p1),
			"INSERT INTO platform.rls_probe_records (tenant_id, patient_id, note) VALUES ('"+c+"', '"+a.p2.String()+"', 'forged')")
		if pgCode(err) != codeInsufficientPrivilege {
			t.Errorf("patient writes another patient's record: %v, want 42501", err)
		}
	})

	t.Run("no or unknown actor_kind sees zero rows", func(t *testing.T) {
		// WithinTx не даст открыть транзакцию без субъекта; проверяем сам
		// fail-closed БД, сбросив параметр внутри транзакции.
		for _, kind := range []string{"", "admin", "STAFF"} {
			var got []string
			err := inTx(ctx, env.db, scope.Tenant(a.clinic, scope.System()), func(ctx context.Context, q postgres.DBTX) error {
				if _, err := q.Exec(ctx, "SELECT set_config('app.actor_kind', $1, true)", kind); err != nil {
					return err
				}
				for _, table := range []string{"rls_probe", "rls_probe_public", "rls_probe_patients", "rls_probe_records"} {
					var n int
					if err := q.QueryRow(ctx, "SELECT count(*) FROM platform."+table).Scan(&n); err != nil {
						return err
					}
					if n != 0 {
						got = append(got, table)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("actor_kind %q sees rows in %v", kind, got)
			}
		}
	})
}
