package postgres

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// migrateLockKey — ключ pg_advisory_lock. Два мигратора (два пода при
// раскатке, CI и человек) не должны применять миграции одновременно.
// Значение — байты "neuroden" как int64: фиксированное и узнаваемое
// в pg_locks.
const migrateLockKey int64 = 0x6e6575726f64656e

// migrationLockTimeout — lock_timeout внутри миграции. ALTER TABLE ждёт
// ACCESS EXCLUSIVE, и пока он ждёт, за ним в очередь встают все запросы
// приложения к этой таблице. Лучше упасть миграцией и повторить, чем
// положить API на время ожидания.
const migrationLockTimeout = "5s"

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

type appliedMigration struct {
	version  int
	name     string
	checksum string
}

// Migrate применяет к БД недостающие миграции из fsys.
//
// Формат файлов: NNNN_name.sql, версии идут подряд с 0001. Каждая миграция
// выполняется в своей транзакции вместе с записью в schema_migrations, так
// что она применена целиком или не применена вовсе. Миграции только вперёд:
// откат медицинских данных — новая миграция, а не down-скрипт.
//
// Перед применением проверяется, что уже применённые миграции не изменены
// (sha256 содержимого) и что БД не новее бинарника.
//
// conn должен принадлежать владельцу схемы: прикладная роль DDL не выполняет.
func Migrate(ctx context.Context, conn *pgx.Conn, fsys fs.FS, log *slog.Logger) error {
	migs, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Сессионный lock снимется и при закрытии соединения; явное снятие —
		// чтобы следующий мигратор не ждал, пока мы закроемся.
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
			log.WarnContext(ctx, "release migration lock failed", "err", err)
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		checksum   text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := readApplied(ctx, conn)
	if err != nil {
		return err
	}
	if err := verifyApplied(migs, applied); err != nil {
		return err
	}

	for _, m := range migs[len(applied):] {
		if err := applyMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
		log.InfoContext(ctx, "migration applied", "version", m.version, "name", m.name)
	}
	log.InfoContext(ctx, "migrations up to date", "version", len(migs))
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, m migration) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
			return fmt.Errorf("set lock_timeout: %w", err)
		}
		// Exec без аргументов идёт по simple protocol: в файле может быть
		// несколько statement'ов.
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO public.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
			m.version, m.name, m.checksum); err != nil {
			return fmt.Errorf("record migration: %w", err)
		}
		return nil
	})
}

func readApplied(ctx context.Context, conn *pgx.Conn) ([]appliedMigration, error) {
	rows, err := conn.Query(ctx, "SELECT version, name, checksum FROM public.schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (appliedMigration, error) {
		var a appliedMigration
		err := r.Scan(&a.version, &a.name, &a.checksum)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

// verifyApplied проверяет, что применённые миграции — префикс известных
// и не изменены после применения.
func verifyApplied(migs []migration, applied []appliedMigration) error {
	for i, a := range applied {
		if i >= len(migs) {
			return fmt.Errorf("database has migration %04d_%s unknown to this binary: deploy a newer build", a.version, a.name)
		}
		m := migs[i]
		if a.version != m.version {
			return fmt.Errorf("applied migrations are not contiguous: expected %04d, found %04d", m.version, a.version)
		}
		if a.checksum != m.checksum || a.name != m.name {
			return fmt.Errorf("migration %04d_%s was modified after being applied: add a new migration instead", m.version, m.name)
		}
	}
	return nil
}

// loadMigrations читает *.sql из fsys и проверяет имена и нумерацию.
// Файл .sql с неверным именем — ошибка, а не пропуск: опечатка в имени
// не должна молча оставить миграцию неприменённой.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(data)
		migs = append(migs, migration{version: version, name: name, sql: string(data), checksum: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(migs, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	for i, m := range migs {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 0001: expected %04d, found %04d_%s", i+1, m.version, m.name)
		}
	}
	return migs, nil
}

var errMigrationName = errors.New("migration file name must match NNNN_lower_snake_name.sql")

func parseMigrationName(file string) (int, string, error) {
	base := strings.TrimSuffix(file, ".sql")
	num, name, ok := strings.Cut(base, "_")
	if !ok || len(num) != 4 || name == "" {
		return 0, "", fmt.Errorf("%s: %w", file, errMigrationName)
	}
	version, err := strconv.Atoi(num)
	if err != nil || version < 1 {
		return 0, "", fmt.Errorf("%s: %w", file, errMigrationName)
	}
	for i := range len(name) {
		if !isSnakeChar(name[i]) {
			return 0, "", fmt.Errorf("%s: %w", file, errMigrationName)
		}
	}
	return version, name, nil
}

func isSnakeChar(c byte) bool {
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_'
}
