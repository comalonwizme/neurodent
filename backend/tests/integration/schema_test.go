//go:build integration

package integration

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// policySpec — политика в шаблоне класса: имя, permissive/restrictive,
// команда (pg_policy.polcmd: '*' все, 'r' SELECT, 'a' INSERT, 'w' UPDATE,
// 'd' DELETE).
type policySpec struct {
	name       string
	permissive bool
	cmd        string
}

func (p policySpec) String() string {
	kind := "restrictive"
	if p.permissive {
		kind = "permissive"
	}
	return fmt.Sprintf("%s(%s,%s)", p.name, kind, p.cmd)
}

// classTemplates — шаблоны политик классов (ADR-0011). Набор сверяется
// точно: лишняя permissive-политика расширяет доступ — это ошибка CI.
func classTemplates() map[string][]policySpec {
	tenantGuard := policySpec{"tenant_guard", false, "*"}
	subjectGuard := policySpec{"subject_guard", false, "*"}
	staffAccess := policySpec{"staff_access", true, "*"}
	patientRead := policySpec{"patient_read", true, "r"}
	return map[string][]policySpec{
		"tenant":           {tenantGuard, staffAccess},
		"tenant-public":    {tenantGuard, staffAccess, patientRead},
		"patient-registry": {tenantGuard, subjectGuard, staffAccess, patientRead},
		"patient-owned":    {tenantGuard, subjectGuard, staffAccess, {"patient_access", true, "*"}},
		"cell-global":      nil,
	}
}

// rlsClasses — классы, которым обязательны ENABLE + FORCE RLS.
func rlsClasses() []string {
	return []string{"tenant", "tenant-public", "patient-registry", "patient-owned"}
}

type tableInfo struct {
	name             string
	class            string
	rls, force       bool
	hasPatientColumn bool
	policies         []policySpec
}

// loadTables читает из каталога все таблицы вне public и системных схем.
func loadTables(t *testing.T, env *pgEnv) []tableInfo {
	t.Helper()
	ctx := t.Context()
	conn := ownerConn(t, env)
	rows, err := conn.Query(ctx, `
		SELECT c.oid, n.nspname || '.' || c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       coalesce(obj_description(c.oid, 'pg_class'), ''),
		       EXISTS (SELECT 1 FROM pg_attribute a
		               WHERE a.attrelid = c.oid AND a.attname = 'patient_id' AND NOT a.attisdropped)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p')
		  AND n.nspname NOT IN ('public', 'pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		ORDER BY 2`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		oid     uint32
		table   tableInfo
		comment string
	}
	raw, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.oid, &x.table.name, &x.table.rls, &x.table.force, &x.comment, &x.table.hasPatientColumn)
		return x, err
	})
	if err != nil {
		t.Fatal(err)
	}

	tables := make([]tableInfo, 0, len(raw))
	for _, x := range raw {
		if cls, ok := strings.CutPrefix(x.comment, "class="); ok {
			x.table.class, _, _ = strings.Cut(cls, ";")
		}
		prows, err := conn.Query(ctx, `SELECT polname, polpermissive, polcmd::text FROM pg_policy WHERE polrelid = $1 ORDER BY polname`, x.oid)
		if err != nil {
			t.Fatal(err)
		}
		x.table.policies, err = pgx.CollectRows(prows, func(r pgx.CollectableRow) (policySpec, error) {
			var p policySpec
			err := r.Scan(&p.name, &p.permissive, &p.cmd)
			return p, err
		})
		if err != nil {
			t.Fatal(err)
		}
		tables = append(tables, x.table)
	}
	return tables
}

// schemaViolations проверяет правила классификации ADR-0011.
func schemaViolations(tables []tableInfo) []string {
	templates := classTemplates()
	var out []string
	for _, tb := range tables {
		want, known := templates[tb.class]
		switch {
		case tb.class == "":
			out = append(out, tb.name+": no class= in COMMENT ON TABLE")
			continue
		case !known:
			// Классы со своими ADR (audit — 0014) проверяются своими тестами.
			if tb.class != "audit" {
				out = append(out, tb.name+": unknown class "+tb.class)
			}
			continue
		}
		if slices.Contains(rlsClasses(), tb.class) && (!tb.rls || !tb.force) {
			out = append(out, fmt.Sprintf("%s: class %s needs ENABLE and FORCE ROW LEVEL SECURITY (rls=%v force=%v)", tb.name, tb.class, tb.rls, tb.force))
		}
		got := slices.SortedFunc(slices.Values(tb.policies), comparePolicy)
		exp := slices.SortedFunc(slices.Values(want), comparePolicy)
		if !slices.Equal(got, exp) {
			out = append(out, fmt.Sprintf("%s: policies %v, class %s requires exactly %v", tb.name, got, tb.class, exp))
		}
		if tb.hasPatientColumn && (tb.class == "tenant" || tb.class == "tenant-public") {
			out = append(out, fmt.Sprintf("%s: has patient_id, so it must be patient-owned, not %s", tb.name, tb.class))
		}
	}
	return out
}

func comparePolicy(a, b policySpec) int { return strings.Compare(a.name, b.name) }

func testSchema(t *testing.T, env *pgEnv) {
	t.Run("every table follows its class template", func(t *testing.T) {
		tables := loadTables(t, env)
		if len(tables) == 0 {
			t.Fatal("no tables found")
		}
		for _, v := range schemaViolations(tables) {
			t.Error(v)
		}
	})

	// Проверка самой проверки: на синтетических описаниях таблиц каждое
	// правило срабатывает.
	t.Run("checker catches every rule", func(t *testing.T) {
		good := classTemplates()
		cases := []struct {
			name  string
			table tableInfo
			want  string
		}{
			{"no class", tableInfo{name: "x.a"}, "no class="},
			{"unknown class", tableInfo{name: "x.b", class: "tenant-ish"}, "unknown class"},
			{"rls off", tableInfo{name: "x.c", class: "tenant", force: true, policies: good["tenant"]}, "needs ENABLE and FORCE"},
			{"force off", tableInfo{name: "x.d", class: "tenant", rls: true, policies: good["tenant"]}, "needs ENABLE and FORCE"},
			{"extra permissive policy", tableInfo{name: "x.e", class: "tenant", rls: true, force: true,
				policies: append(slices.Clone(good["tenant"]), policySpec{"open", true, "*"})}, "requires exactly"},
			{"guard made permissive", tableInfo{name: "x.f", class: "patient-owned", rls: true, force: true,
				policies: []policySpec{{"tenant_guard", false, "*"}, {"subject_guard", true, "*"}, {"staff_access", true, "*"}, {"patient_access", true, "*"}}}, "requires exactly"},
			{"patient_id in tenant table", tableInfo{name: "x.g", class: "tenant", rls: true, force: true,
				hasPatientColumn: true, policies: good["tenant"]}, "must be patient-owned"},
			{"policies on cell-global", tableInfo{name: "x.h", class: "cell-global",
				policies: []policySpec{{"x", true, "*"}}}, "requires exactly"},
		}
		for _, tt := range cases {
			got := schemaViolations([]tableInfo{tt.table})
			if len(got) != 1 || !strings.Contains(got[0], tt.want) {
				t.Errorf("%s: violations = %v, want one containing %q", tt.name, got, tt.want)
			}
		}
	})
}
