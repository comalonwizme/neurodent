package arch_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/tests/arch"
)

const fixtureModule = "example.com/fx"

// TestRepository — главный тест: реальный код проходит все правила.
func TestRepository(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	firstLine, _, _ := strings.Cut(string(gomod), "\n")
	module := strings.TrimSpace(strings.TrimPrefix(firstLine, "module"))

	violations, err := arch.Check(root, module)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestFixtures проверяет саму проверку: каждая фикстура-нарушитель даёт
// ровно ожидаемые нарушения, чистая — ни одного.
func TestFixtures(t *testing.T) {
	want := map[string][]string{
		"clean":                    nil,
		"boundary_internal":        {arch.RuleModuleBoundary},
		"boundary_root":            {arch.RuleModuleBoundary},
		"module_root_outside_app":  {arch.RuleModuleRoot},
		"contract_pgx":             {arch.RuleContract},
		"contract_nethttp":         {arch.RuleContract},
		"contract_own_internal":    {arch.RuleContract},
		"domain_nethttp":           {arch.RuleDomain},
		"domain_os":                {arch.RuleDomain},
		"domain_slog":              {arch.RuleDomain},
		"domain_platform":          {arch.RuleDomain},
		"domain_app":               {arch.RuleDomain},
		"app_pgx":                  {arch.RuleApp},
		"app_platform":             {arch.RuleApp},
		"app_adapter":              {arch.RuleApp},
		"ports_app":                {arch.RulePorts},
		"adapter_pg_http":          {arch.RuleAdapterPG},
		"adapter_http_pgx":         {arch.RuleAdapterHTTP},
		"adapter_http_platform_pg": {arch.RuleAdapterHTTP},
		"platform_module":          {arch.RuleLayer},
		"shared_platform":          {arch.RuleLayer},
		"shared_external":          {arch.RuleLayer},
		"time_now_domain":          {arch.RuleTimeNow},
		"time_since_app_alias":     {arch.RuleTimeNow},
		"time_now_in_test":         {arch.RuleTimeNow},
		"module_cycle":             {arch.RuleModuleCycle},
		"unknown_package":          {arch.RuleModuleLayout},
		"unknown_adapter":          {arch.RuleModuleLayout},
		"unknown_internal_root":    {arch.RuleModuleLayout},
	}

	dirs, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if _, ok := want[d.Name()]; !ok {
			t.Errorf("fixture %s has no expectation", d.Name())
		}
	}
	for name, rules := range want {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			violations, err := arch.Check(filepath.Join("testdata", name), fixtureModule)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, v := range violations {
				got = append(got, v.Rule)
			}
			if !slices.Equal(got, rules) {
				t.Errorf("rules = %v, want %v\nviolations:\n%v", got, rules, violations)
			}
		})
	}
}
