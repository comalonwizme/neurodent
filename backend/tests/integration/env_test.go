//go:build integration

// Package integration — тесты платформы против настоящего Postgres в Docker
// (testcontainers). Запуск: make test-integration.
//
// Один контейнер на прогон: TestPostgres поднимает его и передаёт окружение
// в подтесты замыканием (без глобального состояния). У каждого подтеста
// свои случайные клиники и пользователи, поэтому общая БД им не мешает.
package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
	"github.com/comalonwizme/neurodent/backend/migrations"
)

// postgresImage — тот же образ, что в docker-compose.yml (сверяется тестом).
const postgresImage = "postgres:18.6-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"

// SQLSTATE, которые проверяют тесты.
const (
	codeInsufficientPrivilege = "42501" // must be owner, permission denied, нарушение RLS
	codeUniqueViolation       = "23505"
	codeCheckViolation        = "23514"
	codeQueryCanceled         = "57014"
	codeDivisionByZero        = "22012"
)

type pgEnv struct {
	ownerDSN string
	appDSN   string
	db       *postgres.DB // прикладная роль
	log      *slog.Logger
}

func TestPostgres(t *testing.T) {
	env := startPostgres(t)
	t.Run("migrations", func(t *testing.T) { testMigrations(t, env) })
	t.Run("transactions", func(t *testing.T) { testTransactions(t, env) })
	t.Run("rls", func(t *testing.T) { testRLS(t, env) })
	t.Run("schema", func(t *testing.T) { testSchema(t, env) })
	t.Run("classes", func(t *testing.T) { testClasses(t, env) })
	t.Run("sqlc", func(t *testing.T) { testSQLC(t, env) })
	t.Run("audit", func(t *testing.T) { testAudit(t, env) })
	t.Run("ratelimit", func(t *testing.T) { testRateLimit(t, env) })
	t.Run("pool", func(t *testing.T) { testPool(t, env) })
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

	if err := postgres.Migrate(ctx, ownerConn(t, env), migrations.FS(), env.log); err != nil {
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

func newClinic() tenant.ID { return tenant.FromID(id.New()) }

// asStaff — scope сотрудника (нового случайного пользователя) клиники c.
func asStaff(c tenant.ID) scope.Scope { return scope.Tenant(c, scope.Staff(id.New())) }

// inTx выполняет fn в транзакции со scope sc, отдавая DBTX текущей
// транзакции — так, как это делает адаптер модуля.
func inTx(ctx context.Context, db *postgres.DB, sc scope.Scope, fn func(ctx context.Context, q postgres.DBTX) error) error {
	return db.WithinTx(ctx, sc, func(ctx context.Context) error {
		q, err := postgres.Tx(ctx)
		if err != nil {
			return err
		}
		return fn(ctx, q)
	})
}

// exec выполняет запросы по очереди в одной транзакции.
func exec(ctx context.Context, db *postgres.DB, sc scope.Scope, sqls ...string) error {
	return inTx(ctx, db, sc, func(ctx context.Context, q postgres.DBTX) error {
		for _, s := range sqls {
			if _, err := q.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
}

// strings1 возвращает первый столбец (text) всех строк запроса.
func strings1(t *testing.T, ctx context.Context, db *postgres.DB, sc scope.Scope, sql string, args ...any) []string {
	t.Helper()
	var got []string
	err := inTx(ctx, db, sc, func(ctx context.Context, q postgres.DBTX) error {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		got, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return got
}

func pgCode(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}
