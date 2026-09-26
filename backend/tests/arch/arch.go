// Package arch проверяет правила зависимостей из ADR-0012 по исходникам:
// импорты каждого .go файла (включая тесты и файлы под build-тегами),
// ацикличность графа модулей и запрет time.Now в domain/ и app/.
//
// Только stdlib (go/parser): файлам не нужно компилироваться, поэтому
// фикстуры-нарушители в testdata — просто файлы с нужными импортами.
package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Violation — одно нарушение: файл, правило, подробность.
type Violation struct {
	File   string
	Rule   string
	Detail string
}

func (v Violation) String() string { return fmt.Sprintf("%s: [%s] %s", v.File, v.Rule, v.Detail) }

// Правила. Имена стабильны: на них ссылаются тесты и фикстуры.
const (
	RuleModuleBoundary = "module-boundary"   // модуль A → модуль B только через B/contract
	RuleModuleRoot     = "module-root"       // module.go модуля импортирует только internal/app
	RuleContract       = "contract-deps"     // contract: stdlib + shared
	RuleDomain         = "domain-deps"       // domain: stdlib без I/O + shared + свой domain
	RuleApp            = "app-deps"          // app: без pgx, net/http, platform, adapters
	RulePorts          = "ports-deps"        // ports: stdlib + shared + свой domain
	RuleAdapterPG      = "adapter-pg-deps"   // adapters/postgres: без net/http и adapters/http
	RuleAdapterHTTP    = "adapter-http-deps" // adapters/http: без pgx и adapters/postgres
	RuleLayer          = "layer"             // platform и shared не видят modules; shared — только stdlib + shared
	RuleModuleCycle    = "module-cycle"      // граф модулей ацикличен
	RuleTimeNow        = "time-now"          // domain и app берут время из shared/clock
)

type role int

const (
	roleOther role = iota
	roleWiring
	rolePlatform
	roleShared
	roleModuleRoot
	roleContract
	roleDomain
	roleApp
	rolePorts
	roleAdapterPG
	roleAdapterHTTP
	roleModuleOther
)

// pkg — пакет в терминах правил: роль и модуль (для пакетов модулей).
type pkg struct {
	role   role
	module string
}

// classify определяет роль пакета по пути относительно корня модуля Go.
func classify(rel string) pkg {
	switch {
	case rel == "internal/app" || strings.HasPrefix(rel, "internal/app/"):
		return pkg{role: roleWiring}
	case strings.HasPrefix(rel, "internal/platform/"):
		return pkg{role: rolePlatform}
	case strings.HasPrefix(rel, "internal/shared/"):
		return pkg{role: roleShared}
	}
	rest, ok := strings.CutPrefix(rel, "internal/modules/")
	if !ok {
		return pkg{role: roleOther}
	}
	module, sub, _ := strings.Cut(rest, "/")
	p := pkg{module: module}
	switch {
	case sub == "":
		p.role = roleModuleRoot
	case sub == "contract" || strings.HasPrefix(sub, "contract/"):
		p.role = roleContract
	case under(sub, "internal/domain"):
		p.role = roleDomain
	case under(sub, "internal/app"):
		p.role = roleApp
	case under(sub, "internal/ports"):
		p.role = rolePorts
	case under(sub, "internal/adapters/postgres"):
		p.role = roleAdapterPG
	case under(sub, "internal/adapters/http"):
		p.role = roleAdapterHTTP
	default:
		p.role = roleModuleOther
	}
	return p
}

func under(p, prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }

// Check обходит дерево root (корень модуля Go с путём modulePath) и
// возвращает нарушения. Каталоги testdata, vendor, скрытые и вложенные
// модули (свой go.mod) пропускаются.
func Check(root, modulePath string) ([]Violation, error) {
	c := &checker{module: modulePath, edges: map[string]map[string]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || fileExists(filepath.Join(p, "go.mod"))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		relFile, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return c.file(p, filepath.ToSlash(relFile))
	})
	if err != nil {
		return nil, err
	}
	c.cycles()
	slices.SortFunc(c.out, func(a, b Violation) int {
		return strings.Compare(a.String(), b.String())
	})
	return c.out, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

type checker struct {
	module string
	out    []Violation
	// edges[A][B] — модуль A импортирует контракт B (значение — файл-пример).
	edges map[string]map[string]string
}

func (c *checker) add(file, rule, format string, args ...any) {
	c.out = append(c.out, Violation{File: file, Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

func (c *checker) file(abs, rel string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", rel, err)
	}
	from := classify(path.Dir(rel))
	timeNames := map[string]bool{}
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if imp == "time" {
			name := "time"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			timeNames[name] = true
		}
		c.importRule(rel, from, imp)
	}
	if from.role == roleDomain || from.role == roleApp {
		c.timeNow(rel, fset, f, timeNames)
	}
	return nil
}

// target описывает импорт: внутренний пакет модуля Go (с ролью) или внешний.
type target struct {
	internal bool
	pkg      pkg
	rel      string
}

func (c *checker) target(imp string) target {
	rel, ok := strings.CutPrefix(imp, c.module+"/")
	if !ok {
		return target{}
	}
	return target{internal: true, pkg: classify(rel), rel: rel}
}

func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func isPgx(imp string) bool { return strings.HasPrefix(imp, "github.com/jackc/pgx") }

func isNetHTTP(imp string) bool { return imp == "net/http" || strings.HasPrefix(imp, "net/http/") }

// ioStdlib — пакеты stdlib с вводом-выводом, запрещённые домену.
func ioStdlib(imp string) bool {
	for _, p := range []string{"net", "database", "os", "log"} {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return true
		}
	}
	return false
}

func (c *checker) importRule(file string, from pkg, imp string) {
	t := c.target(imp)

	// Границы модулей: из модуля A в модуль B — только B/contract.
	if from.module != "" && t.internal && t.pkg.module != "" && t.pkg.module != from.module {
		if t.pkg.role != roleContract {
			c.add(file, RuleModuleBoundary, "module %s imports %s: other modules only via their contract/", from.module, imp)
			return
		}
		if c.edges[from.module] == nil {
			c.edges[from.module] = map[string]string{}
		}
		c.edges[from.module][t.pkg.module] = file
	}
	// module.go модуля видит только composition root.
	if t.internal && t.pkg.role == roleModuleRoot && from.role != roleWiring && from.module != t.pkg.module {
		if from.module == "" {
			c.add(file, RuleModuleRoot, "%s is imported outside internal/app", imp)
		}
		return
	}

	switch from.role {
	case rolePlatform, roleShared:
		if t.internal && t.pkg.module != "" {
			c.add(file, RuleLayer, "platform/shared must not import modules: %s", imp)
			return
		}
		if from.role == roleShared && !isStdlib(imp) && (!t.internal || t.pkg.role != roleShared) {
			c.add(file, RuleLayer, "shared imports only stdlib and shared: %s", imp)
		}
	case roleContract:
		ok := (isStdlib(imp) && !isNetHTTP(imp)) || (t.internal && (t.pkg.role == roleShared ||
			(t.pkg.role == roleContract && t.pkg.module == from.module)))
		if !ok {
			c.add(file, RuleContract, "contract imports only stdlib and shared: %s", imp)
		}
	case roleDomain:
		ok := (isStdlib(imp) && !ioStdlib(imp)) || (t.internal && (t.pkg.role == roleShared ||
			(t.pkg.role == roleDomain && t.pkg.module == from.module)))
		if !ok {
			c.add(file, RuleDomain, "domain imports only stdlib without I/O, shared and its own domain: %s", imp)
		}
	case roleApp:
		bad := isPgx(imp) || isNetHTTP(imp) || (t.internal && (t.pkg.role == rolePlatform ||
			t.pkg.role == roleAdapterPG || t.pkg.role == roleAdapterHTTP || t.pkg.role == roleWiring))
		if bad {
			c.add(file, RuleApp, "app must not import pgx, net/http, platform or adapters: %s", imp)
		}
	case rolePorts:
		ok := (isStdlib(imp) && !ioStdlib(imp)) || (t.internal && (t.pkg.role == roleShared ||
			((t.pkg.role == roleDomain || t.pkg.role == rolePorts) && t.pkg.module == from.module)))
		if !ok {
			c.add(file, RulePorts, "ports import only stdlib, shared and their own domain: %s", imp)
		}
	case roleAdapterPG:
		if isNetHTTP(imp) || (t.internal && t.pkg.role == roleAdapterHTTP) {
			c.add(file, RuleAdapterPG, "postgres adapter must not import HTTP: %s", imp)
		}
	case roleAdapterHTTP:
		if isPgx(imp) || (t.internal && (t.pkg.role == roleAdapterPG || t.rel == "internal/platform/postgres")) {
			c.add(file, RuleAdapterHTTP, "http adapter must not import the database layer: %s", imp)
		}
	default:
	}
}

// timeNow ищет вызовы time.Now, time.Since, time.Until (с учётом алиаса
// импорта): в domain и app время приходит из shared/clock.
func (c *checker) timeNow(file string, fset *token.FileSet, f *ast.File, names map[string]bool) {
	if len(names) == 0 {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || !names[x.Name] {
			return true
		}
		switch sel.Sel.Name {
		case "Now", "Since", "Until":
			c.add(file, RuleTimeNow, "line %d: time.%s: use shared/clock", fset.Position(sel.Pos()).Line, sel.Sel.Name)
		default:
		}
		return true
	})
}

// cycles сообщает о циклах в графе «модуль → контракт модуля».
func (c *checker) cycles() {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(m string)
	visit = func(m string) {
		color[m] = grey
		stack = append(stack, m)
		for _, next := range slices.Sorted(maps.Keys(c.edges[m])) {
			switch color[next] {
			case white:
				visit(next)
			case grey:
				i := slices.Index(stack, next)
				cycle := append(slices.Clone(stack[i:]), next)
				c.add(c.edges[m][next], RuleModuleCycle, "module dependency cycle: %s", strings.Join(cycle, " → "))
			default:
			}
		}
		stack = stack[:len(stack)-1]
		color[m] = black
	}
	for _, m := range slices.Sorted(maps.Keys(c.edges)) {
		if color[m] == white {
			visit(m)
		}
	}
}
