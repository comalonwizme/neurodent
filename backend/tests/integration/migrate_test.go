//go:build integration

package integration

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/migrations"
)

// embeddedMigrations копирует встроенные миграции в MapFS, чтобы тест мог
// дописать к ним свою.
func embeddedMigrations(t *testing.T) (fstest.MapFS, int) {
	t.Helper()
	names, err := fs.Glob(migrations.FS(), "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	for _, n := range names {
		data, err := fs.ReadFile(migrations.FS(), n)
		if err != nil {
			t.Fatal(err)
		}
		m[n] = &fstest.MapFile{Data: data}
	}
	return m, len(names)
}

func testMigrations(t *testing.T, env *pgEnv) {
	_, total := embeddedMigrations(t)

	t.Run("idempotent and serialized", func(t *testing.T) {
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
		if n != total {
			t.Errorf("schema_migrations has %d rows, want %d", n, total)
		}
	})

	t.Run("modified migration is rejected", func(t *testing.T) {
		fsys, _ := embeddedMigrations(t)
		fsys["0001_platform_rls_probe.sql"] = &fstest.MapFile{Data: []byte("SELECT 1")}
		err := postgres.Migrate(t.Context(), ownerConn(t, env), fsys, env.log)
		if err == nil || !strings.Contains(err.Error(), "modified after being applied") {
			t.Errorf("Migrate() = %v, want checksum mismatch", err)
		}
	})

	t.Run("failed migration is rolled back entirely", func(t *testing.T) {
		ctx := t.Context()
		fsys, n := embeddedMigrations(t)
		// Первый statement успешен, второй падает: транзакция миграции
		// должна откатить оба и не записать версию.
		broken := fmt.Sprintf("%04d_broken.sql", n+1)
		fsys[broken] = &fstest.MapFile{Data: []byte("CREATE TABLE platform.half_done (id int); SELECT 1/0;")}

		conn := ownerConn(t, env)
		err := postgres.Migrate(ctx, conn, fsys, env.log)
		if pgCode(err) != codeDivisionByZero {
			t.Fatalf("Migrate() = %v, want the migration's own error", err)
		}
		if !strings.Contains(err.Error(), strings.TrimSuffix(broken, ".sql")) {
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
		if tableExists || applied != total {
			t.Errorf("after failed %s: half_done exists=%v, applied=%d; want no table and %d applied", broken, tableExists, applied, total)
		}
	})

	t.Run("database ahead of binary is rejected", func(t *testing.T) {
		err := postgres.Migrate(t.Context(), ownerConn(t, env), fstest.MapFS{}, env.log)
		if err == nil || !strings.Contains(err.Error(), "unknown to this binary") {
			t.Errorf("Migrate() = %v, want database-ahead error", err)
		}
	})
}
