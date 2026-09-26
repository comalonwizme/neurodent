package postgres // white-box: loadMigrations, verifyApplied и checkTLS не экспортируются

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/comalonwizme/neurodent/backend/migrations"
)

const password = "Sup3rS3cret-pw"

// closedPort возвращает адрес, на котором гарантированно никто не слушает:
// порт взят у ОС и сразу освобождён. Соединение получит connection refused
// мгновенно, без таймаутов.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func baseOptions(dsn string) Options {
	return Options{
		DSN:              dsn,
		MaxConns:         2,
		ConnectTimeout:   2 * time.Second,
		StatementTimeout: time.Second,
		MaxConnLifetime:  time.Minute,
		ApplicationName:  "test",
	}
}

func TestNew_InvalidDSNDoesNotLeak(t *testing.T) {
	tests := []string{
		"postgres://user:" + password + "@host:notaport/db",
		"postgres://user:" + password + "@host/db?sslmode=bogus",
		"host=h password=" + password + " sslmode=bogus",
		"postgres://user:" + password + "@[::1/db",
	}
	for _, dsn := range tests {
		_, err := New(t.Context(), baseOptions(dsn))
		if !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("New(%q) = %v, want ErrInvalidDSN", dsn, err)
		}
		if err != nil && strings.Contains(err.Error(), password) {
			t.Errorf("error leaks password: %v", err)
		}
	}
}

func TestNew_RequireTLS(t *testing.T) {
	addr := closedPort(t)
	tests := []struct {
		sslmode   string
		plaintext bool
	}{
		{"disable", true},
		{"allow", true},
		{"prefer", true}, // TLS первым, plaintext — запасным
		{"require", false},
		{"verify-full", false},
	}
	for _, tt := range tests {
		t.Run(tt.sslmode, func(t *testing.T) {
			t.Parallel()
			o := baseOptions("postgres://u:" + password + "@" + addr + "/db?sslmode=" + tt.sslmode)
			o.RequireTLS = true
			_, err := New(t.Context(), o)
			if got := errors.Is(err, ErrTLSRequired); got != tt.plaintext {
				t.Errorf("errors.Is(err, ErrTLSRequired) = %v, want %v (err: %v)", got, tt.plaintext, err)
			}
			if err == nil {
				t.Fatal("New() succeeded against a closed port")
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("error leaks password: %v", err)
			}
		})
	}
}

func TestNew_ConnectErrorDoesNotLeakPassword(t *testing.T) {
	_, err := New(t.Context(), baseOptions("postgres://u:"+password+"@"+closedPort(t)+"/db?sslmode=disable"))
	if err == nil {
		t.Fatal("New() succeeded against a closed port")
	}
	if !strings.HasPrefix(err.Error(), "ping: ") {
		t.Errorf("error = %v, want a ping error", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestConnect_Errors(t *testing.T) {
	if _, err := Connect(t.Context(), "postgres://u:"+password+"@h:bad/db", false, time.Second, "t"); !errors.Is(err, ErrInvalidDSN) {
		t.Errorf("invalid DSN: %v", err)
	}
	if _, err := Connect(t.Context(), "postgres://u:p@h/db?sslmode=prefer", true, time.Second, "t"); !errors.Is(err, ErrTLSRequired) {
		t.Errorf("plaintext fallback allowed: %v", err)
	}
	_, err := Connect(t.Context(), "postgres://u:"+password+"@"+closedPort(t)+"/db?sslmode=disable", false, time.Second, "t")
	if err == nil || strings.Contains(err.Error(), password) {
		t.Errorf("connect error = %v: want an error without the password", err)
	}
}

func TestLoadMigrations(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	tests := []struct {
		name    string
		fs      fstest.MapFS
		want    []string // version_name
		wantErr string
	}{
		{name: "empty", fs: fstest.MapFS{}},
		{
			name: "sorted and non-sql ignored",
			fs: fstest.MapFS{
				"0002_b.sql": file("B"), "0001_a.sql": file("A"), "README.md": file("x"), "migrations.go": file("x"),
			},
			want: []string{"1_a", "2_b"},
		},
		{name: "gap", fs: fstest.MapFS{"0001_a.sql": file(""), "0003_c.sql": file("")}, wantErr: "expected 0002"},
		{name: "duplicate version", fs: fstest.MapFS{"0001_a.sql": file(""), "0001_b.sql": file("")}, wantErr: "expected 0002"},
		{name: "starts at zero", fs: fstest.MapFS{"0000_a.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
		{name: "starts at two", fs: fstest.MapFS{"0002_a.sql": file("")}, wantErr: "expected 0001"},
		{name: "three digits", fs: fstest.MapFS{"001_a.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
		{name: "no name", fs: fstest.MapFS{"0001_.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
		{name: "uppercase name", fs: fstest.MapFS{"0001_Init.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
		{name: "dash in name", fs: fstest.MapFS{"0001_add-users.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
		{name: "no separator", fs: fstest.MapFS{"0001.sql": file("")}, wantErr: "NNNN_lower_snake_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migs, err := loadMigrations(tt.fs)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadMigrations() = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range migs {
				got = append(got, fmt.Sprintf("%d_%s", m.version, m.name))
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("migrations = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyApplied(t *testing.T) {
	migs := []migration{
		{version: 1, name: "a", checksum: "c1"},
		{version: 2, name: "b", checksum: "c2"},
	}
	tests := []struct {
		name    string
		applied []appliedMigration
		wantErr string
	}{
		{name: "nothing applied", applied: nil},
		{name: "prefix applied", applied: []appliedMigration{{1, "a", "c1"}}},
		{name: "all applied", applied: []appliedMigration{{1, "a", "c1"}, {2, "b", "c2"}}},
		{name: "modified", applied: []appliedMigration{{1, "a", "changed"}}, wantErr: "modified after being applied"},
		{name: "renamed", applied: []appliedMigration{{1, "renamed", "c1"}}, wantErr: "modified after being applied"},
		{name: "database ahead", applied: []appliedMigration{{1, "a", "c1"}, {2, "b", "c2"}, {3, "c", "c3"}}, wantErr: "unknown to this binary"},
		{name: "gap in applied", applied: []appliedMigration{{2, "b", "c2"}}, wantErr: "not contiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := verifyApplied(migs, tt.applied)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("verifyApplied() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("verifyApplied() = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmbeddedMigrationsAreValid: встроенные в бинарник миграции проходят
// те же проверки имён и нумерации, что и на проде.
func TestEmbeddedMigrationsAreValid(t *testing.T) {
	migs, err := loadMigrations(migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations")
	}
	// Каждый параметр app.*, который читают политики, должен выставляться
	// в WithinTx: иначе политика молча видит NULL.
	read := 0
	for _, m := range migs {
		rest := m.sql
		for {
			i := strings.Index(rest, "current_setting('app.")
			if i < 0 {
				break
			}
			rest = rest[i+len("current_setting('"):]
			name, _, _ := strings.Cut(rest, "'")
			read++
			if !strings.Contains(setScopeSQL, "'"+name+"'") {
				t.Errorf("migration %04d reads %s, but WithinTx never sets it", m.version, name)
			}
		}
	}
	if read == 0 {
		t.Error("no migration reads app.* settings: RLS policies are missing")
	}
}
