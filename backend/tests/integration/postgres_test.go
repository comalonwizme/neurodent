//go:build integration

// Package integration — тесты платформы против настоящего Postgres в Docker
// (testcontainers). Запуск: make test-integration.
package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
	"github.com/comalonwizme/neurodent/backend/migrations"
)

// postgresImage — тот же образ, что в docker-compose.yml (сверяется тестом).
const postgresImage = "postgres:18.6-alpine"

// codeInsufficientPrivilege — SQLSTATE 42501: must be owner, permission
// denied, нарушение RLS-политики.
const codeInsufficientPrivilege = "42501"

type pgEnv struct {
	ownerDSN string
	appDSN   string
	db       *postgres.DB // прикладная роль
	log      *slog.Logger
}

// repoRoot — корень репозитория: go test запускается из каталога пакета.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// startPostgres поднимает контейнер с тем же init-скриптом ролей, что и
// docker-compose, накатывает миграции владельцем и открывает пул
// прикладной ролью.
func startPostgres(t *testing.T) *pgEnv {
	t.Helper()
	ctx := t.Context()
	superPw, ownerPw, appPw := rand.Text(), rand.Text(), rand.Text()

	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword(superPw),
		tcpostgres.WithInitScripts(filepath.Join(repoRoot(t), "deploy/postgres/initdb/10-roles.sh")),
		testcontainers.WithEnv(map[string]string{
			"NEURODENT_PG_OWNER_PASSWORD": ownerPw,
			"NEURODENT_PG_APP_PASSWORD":   appPw,
		}),
		tcpostgres.BasicWaitStrategies(),
	)
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	superDSN, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	env := &pgEnv{
		ownerDSN: roleDSN(t, superDSN, "neurodent_owner", ownerPw),
		appDSN:   roleDSN(t, superDSN, "neurodent_app", appPw),
		log:      slog.New(slog.DiscardHandler),
	}

	conn := ownerConn(t, env)
	if err := postgres.Migrate(ctx, conn, migrations.FS(), env.log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	env.db = appDB(t, env, 4)
	return env
}

func roleDSN(t *testing.T, superDSN, user, password string) string {
	t.Helper()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	u.Path = "/neurodent"
	return u.String()
}

func ownerConn(t *testing.T, env *pgEnv) *pgx.Conn {
	t.Helper()
	conn, err := postgres.Connect(t.Context(), env.ownerDSN, false, 10*time.Second, "it-owner")
	if err != nil {
		t.Fatalf("owner connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func appDB(t *testing.T, env *pgEnv, maxConns int32) *postgres.DB {
	t.Helper()
	db, err := postgres.New(t.Context(), postgres.Options{
		DSN:              env.appDSN,
		MaxConns:         maxConns,
		ConnectTimeout:   10 * time.Second,
		StatementTimeout: 1500 * time.Millisecond,
		MaxConnLifetime:  time.Minute,
		ApplicationName:  "it-app",
	})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTenant(t *testing.T) tenant.ID {
	t.Helper()
	// Случайный UUID: тесты делят одну БД, у каждого свои tenant'ы.
	var b [16]byte
	_, _ = rand.Read(b[:])
	id, err := tenant.Parse(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertNote(ctx context.Context, db *postgres.DB, id tenant.ID, note string) error {
	return db.WithinTx(tenant.WithID(ctx, id), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, $2)", id.String(), note)
		return err
	})
}

// visibleTenants возвращает tenant_id всех строк, видимых в транзакции с ctx.
func visibleTenants(t *testing.T, ctx context.Context, db *postgres.DB) []string {
	t.Helper()
	var got []string
	err := db.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT tenant_id::text FROM platform.rls_probe")
		if err != nil {
			return err
		}
		got, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	return got
}

func pgCode(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func TestPostgres(t *testing.T) {
	env := startPostgres(t)

	t.Run("migrations are idempotent and serialized", func(t *testing.T) {
		// Три мигратора одновременно: advisory lock пропускает по одному,
		// повторное применение — no-op.
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i := range errs {
			conn := ownerConn(t, env)
			wg.Go(func() { errs[i] = postgres.Migrate(t.Context(), conn, migrations.FS(), env.log) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("migrator %d: %v", i, err)
			}
		}
		var n int
		if err := ownerConn(t, env).QueryRow(t.Context(), "SELECT count(*) FROM public.schema_migrations").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("schema_migrations has %d rows, want 1", n)
		}
	})

	t.Run("modified migration is rejected", func(t *testing.T) {
		fsys := fstest.MapFS{"0001_platform_rls_probe.sql": {Data: []byte("SELECT 1")}}
		err := postgres.Migrate(t.Context(), ownerConn(t, env), fsys, env.log)
		if err == nil || !strings.Contains(err.Error(), "modified after being applied") {
			t.Errorf("Migrate() = %v, want checksum mismatch", err)
		}
	})

	t.Run("failed migration is rolled back entirely", func(t *testing.T) {
		ctx := t.Context()
		first, err := fs.ReadFile(migrations.FS(), "0001_platform_rls_probe.sql")
		if err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{
			"0001_platform_rls_probe.sql": {Data: first},
			// Первый statement успешен, второй падает: транзакция миграции
			// должна откатить оба и не записать версию.
			"0002_broken.sql": {Data: []byte("CREATE TABLE platform.half_done (id int); SELECT 1/0;")},
		}
		conn := ownerConn(t, env)
		err = postgres.Migrate(ctx, conn, fsys, env.log)
		if pgCode(err) != "22012" { // division_by_zero
			t.Fatalf("Migrate() = %v, want the migration's own error", err)
		}
		if !strings.Contains(err.Error(), "migration 0002_broken") {
			t.Errorf("error does not name the failed migration: %v", err)
		}
		var tableExists bool
		var applied int
		if err := conn.QueryRow(ctx, "SELECT to_regclass('platform.half_done') IS NOT NULL").Scan(&tableExists); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.schema_migrations").Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if tableExists || applied != 1 {
			t.Errorf("after failed 0002: half_done exists=%v, applied=%d; want no table and 1 applied", tableExists, applied)
		}
	})

	t.Run("database ahead of binary is rejected", func(t *testing.T) {
		err := postgres.Migrate(t.Context(), ownerConn(t, env), fstest.MapFS{}, env.log)
		if err == nil || !strings.Contains(err.Error(), "unknown to this binary") {
			t.Errorf("Migrate() = %v, want database-ahead error", err)
		}
	})

	t.Run("tenant sees only its own rows", func(t *testing.T) {
		ctx := t.Context()
		a, b := newTenant(t), newTenant(t)
		for _, id := range []tenant.ID{a, a, b} {
			if err := insertNote(ctx, env.db, id, "isolation"); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []tenant.ID{a, b} {
			got := visibleTenants(t, tenant.WithID(ctx, id), env.db)
			if len(got) == 0 {
				t.Errorf("tenant %s sees none of its rows", id)
			}
			for _, tid := range got {
				if tid != id.String() {
					t.Errorf("tenant %s sees a row of tenant %s", id, tid)
				}
			}
		}
	})

	t.Run("no tenant context sees zero rows even on a reused connection", func(t *testing.T) {
		ctx := t.Context()
		// Одно соединение: вторая транзакция гарантированно идёт по тому же
		// соединению, где до этого был SET LOCAL.
		db := appDB(t, env, 1)
		a := newTenant(t)
		if err := insertNote(ctx, db, a, "reuse"); err != nil {
			t.Fatal(err)
		}
		var setting *string
		err := db.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT current_setting('app.tenant_id', true)").Scan(&setting)
		})
		if err != nil {
			t.Fatal(err)
		}
		// Та самая ловушка: после SET LOCAL параметр на соединении — '', а не
		// NULL. Политика обязана это переживать (NULLIF), иначе ''::uuid упадёт.
		if setting == nil || *setting != "" {
			shown := "NULL"
			if setting != nil {
				shown = fmt.Sprintf("%q", *setting)
			}
			t.Errorf("app.tenant_id on a reused connection = %s, want '' (the case NULLIF exists for)", shown)
		}
		if got := visibleTenants(t, ctx, db); len(got) != 0 {
			t.Errorf("no-tenant transaction sees %d rows, want 0", len(got))
		}
	})

	t.Run("cross-tenant insert is rejected by WITH CHECK", func(t *testing.T) {
		ctx := t.Context()
		a, b := newTenant(t), newTenant(t)
		err := env.db.WithinTx(tenant.WithID(ctx, a), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, 'x')", b.String())
			return err
		})
		if pgCode(err) != codeInsufficientPrivilege {
			t.Errorf("insert for another tenant: err = %v, want SQLSTATE 42501", err)
		}
	})

	t.Run("app role cannot bypass RLS", func(t *testing.T) {
		ctx := t.Context()
		if err := insertNote(ctx, env.db, newTenant(t), "bypass"); err != nil {
			t.Fatal(err)
		}
		attempts := []struct {
			name string
			sql  []string
		}{
			{"disable RLS", []string{"ALTER TABLE platform.rls_probe DISABLE ROW LEVEL SECURITY"}},
			{"drop FORCE", []string{"ALTER TABLE platform.rls_probe NO FORCE ROW LEVEL SECURITY"}},
			{"drop policy", []string{"DROP POLICY tenant_isolation ON platform.rls_probe"}},
			{"add permissive policy", []string{"CREATE POLICY open ON platform.rls_probe USING (true)"}},
			{"row_security off", []string{"SET LOCAL row_security = off", "SELECT count(*) FROM platform.rls_probe"}},
			{"become owner", []string{"SET LOCAL ROLE neurodent_owner"}},
		}
		for _, a := range attempts {
			err := env.db.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
				for _, q := range a.sql {
					if _, err := tx.Exec(ctx, q); err != nil {
						return err
					}
				}
				return nil
			})
			if pgCode(err) != codeInsufficientPrivilege {
				t.Errorf("%s: err = %v, want SQLSTATE 42501", a.name, err)
			}
		}

		var super, bypass bool
		var owner string
		err := env.db.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&super, &bypass); err != nil {
				return err
			}
			return tx.QueryRow(ctx, "SELECT tableowner FROM pg_tables WHERE schemaname = 'platform' AND tablename = 'rls_probe'").Scan(&owner)
		})
		if err != nil {
			t.Fatal(err)
		}
		if super || bypass || owner != "neurodent_owner" {
			t.Errorf("app role: superuser=%v bypassrls=%v; table owner=%s", super, bypass, owner)
		}
	})

	t.Run("FORCE RLS restricts the owner too", func(t *testing.T) {
		ctx := t.Context()
		if err := insertNote(ctx, env.db, newTenant(t), "force"); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := ownerConn(t, env).QueryRow(ctx, "SELECT count(*) FROM platform.rls_probe").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("owner without tenant sees %d rows, want 0 (FORCE ROW LEVEL SECURITY)", n)
		}
	})

	t.Run("WithinTx commits on success and rolls back on error and panic", func(t *testing.T) {
		ctx := t.Context()
		a := newTenant(t)
		tctx := tenant.WithID(ctx, a)
		errBoom := errors.New("boom")

		insert := func(note string) postgres.TxFunc {
			return func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "INSERT INTO platform.rls_probe (tenant_id, note) VALUES ($1, $2)", a.String(), note)
				return err
			}
		}
		if err := env.db.WithinTx(tctx, insert("committed")); err != nil {
			t.Fatal(err)
		}
		err := env.db.WithinTx(tctx, func(ctx context.Context, tx pgx.Tx) error {
			if err := insert("rolled back on error")(ctx, tx); err != nil {
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
			_ = env.db.WithinTx(tctx, func(ctx context.Context, tx pgx.Tx) error {
				if err := insert("rolled back on panic")(ctx, tx); err != nil {
					return err
				}
				panic("db panic")
			})
		}()

		var notes []string
		err = env.db.WithinTx(tctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT note FROM platform.rls_probe ORDER BY note")
			if err != nil {
				return err
			}
			notes, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(notes, ",") != "committed" {
			t.Errorf("tenant rows = %v, want only [committed]", notes)
		}
	})

	t.Run("session settings come from options", func(t *testing.T) {
		var timeout, app string
		err := env.db.WithinTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT current_setting('statement_timeout'), current_setting('application_name')").Scan(&timeout, &app)
		})
		if err != nil {
			t.Fatal(err)
		}
		if timeout != "1500ms" || app != "it-app" {
			t.Errorf("statement_timeout=%s application_name=%s", timeout, app)
		}
	})

	t.Run("statement timeout cancels long queries", func(t *testing.T) {
		err := env.db.WithinTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
			return err
		})
		if pgCode(err) != "57014" { // query_canceled
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
		if err := db.WithinTx(t.Context(), func(context.Context, pgx.Tx) error { return nil }); err == nil || !strings.HasPrefix(err.Error(), "begin tx: ") {
			t.Errorf("WithinTx on closed pool = %v, want begin error", err)
		}
		rec = httptest.NewRecorder()
		probe.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("readyz with closed pool = %d, want 503", rec.Code)
		}
	})
}

// TestComposeUsesSameImage: dev-окружение и тесты работают на одной версии
// Postgres.
func TestComposeUsesSameImage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "image: "+postgresImage) {
		t.Errorf("docker-compose.yml does not use %s", postgresImage)
	}
}
