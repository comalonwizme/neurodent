package config // white-box: нужна неэкспортируемая load и errEmpty

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// lookupFrom подменяет os.LookupEnv. map различает «ключа нет» и
// «ключ есть, значение пустое» — ровно как окружение процесса.
func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// testDSN — значение-маркер: тесты проверяют, что оно не утекает в логи
// и форматированный вывод.
const testDSN = "postgres://app:s3cr3t-pw@db.internal:5432/neurodent"

// testRLKey — секрет HMAC лимитов (32 байта); тоже не должен утекать.
const testRLKey = "rl-key-0123456789abcdef0123456789"

// testOrigins — origin браузерного клиента для окружений вне dev.
const testOrigins = "https://app.neurodent.example"

// withEnv — окружение staging плюс переопределения. Staging выбран базой,
// потому что у него ненулевой DrainDelay и все инварианты работают.
func withEnv(kv ...string) map[string]string {
	m := map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "staging", "NEURODENT_CORS_ALLOWED_ORIGINS": testOrigins}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestLoad_Valid(t *testing.T) {
	// want пишется литералами: тест фиксирует контракт (какие значения получит
	// оператор), а не повторяет константы из config.go.
	tests := []struct {
		name string
		env  map[string]string
		want Config
	}{
		{
			name: "dev on defaults has no drain delay",
			env:  map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "dev"},
			want: Config{
				Env:               "dev",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				HandlerTimeout:    10 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          10,
				DBConnectTimeout:    5 * time.Second,
				DBStatementTimeout:  5 * time.Second,
				DBMaxConnLifetime:   30 * time.Minute,
			},
		},
		{
			name: "staging on defaults drains for 2s",
			env:  map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "staging", "NEURODENT_CORS_ALLOWED_ORIGINS": testOrigins},
			want: Config{
				Env:               "staging",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				HandlerTimeout:    10 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        2 * time.Second,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          10,
				DBConnectTimeout:    5 * time.Second,
				DBStatementTimeout:  5 * time.Second,
				CORSAllowedOrigins:  Origins{joined: testOrigins},
				DBMaxConnLifetime:   30 * time.Minute,
			},
		},
		{
			name: "prod with info level",
			env:  map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "prod", "NEURODENT_CORS_ALLOWED_ORIGINS": testOrigins, "NEURODENT_LOG_LEVEL": "info"},
			want: Config{
				Env:               "prod",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				HandlerTimeout:    10 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        2 * time.Second,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          10,
				DBConnectTimeout:    5 * time.Second,
				DBStatementTimeout:  5 * time.Second,
				CORSAllowedOrigins:  Origins{joined: testOrigins},
				DBMaxConnLifetime:   30 * time.Minute,
			},
		},
		{
			name: "dev with every variable customized",
			env: map[string]string{
				"NEURODENT_DB_DSN":               testDSN,
				"NEURODENT_RATELIMIT_KEY":        testRLKey,
				"NEURODENT_ENV":                  "dev",
				"NEURODENT_HTTP_ADDR":            "0.0.0.0:9090",
				"NEURODENT_READ_HEADER_TIMEOUT":  "2s",
				"NEURODENT_READ_TIMEOUT":         "30s",
				"NEURODENT_WRITE_TIMEOUT":        "20s",
				"NEURODENT_HANDLER_TIMEOUT":      "8s",
				"NEURODENT_IDLE_TIMEOUT":         "90s",
				"NEURODENT_SHUTDOWN_TIMEOUT":     "20s",
				"NEURODENT_DRAIN_DELAY":          "3s",
				"NEURODENT_LOG_LEVEL":            "debug",
				"NEURODENT_DB_MAX_CONNS":         "25",
				"NEURODENT_DB_CONNECT_TIMEOUT":   "3s",
				"NEURODENT_DB_STATEMENT_TIMEOUT": "2500ms",
				"NEURODENT_DB_MAX_CONN_LIFETIME": "1h",
			},
			want: Config{
				Env:               "dev",
				HTTPAddr:          "0.0.0.0:9090",
				ReadHeaderTimeout: 2 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      20 * time.Second,
				HandlerTimeout:    8 * time.Second,
				IdleTimeout:       90 * time.Second,
				ShutdownTimeout:   20 * time.Second,
				DrainDelay:        3 * time.Second,
				LogLevel:          slog.LevelDebug,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          25,
				DBConnectTimeout:    3 * time.Second,
				DBStatementTimeout:  2500 * time.Millisecond,
				DBMaxConnLifetime:   time.Hour,
			},
		},
		{
			// HANDLER < WRITE, поэтому WRITE = 1s в валидном конфиге недостижим:
			// берём ближайшее допустимое значение (min для WRITE — в TestLoad_Errors).
			name: "all ranges at min, write just above handler",
			env: withEnv(
				"NEURODENT_HTTP_ADDR", ":1",
				"NEURODENT_READ_HEADER_TIMEOUT", "1s",
				"NEURODENT_READ_TIMEOUT", "1s",
				"NEURODENT_WRITE_TIMEOUT", "1001ms",
				"NEURODENT_HANDLER_TIMEOUT", "1s",
				"NEURODENT_IDLE_TIMEOUT", "10s",
				"NEURODENT_SHUTDOWN_TIMEOUT", "1001ms",
				"NEURODENT_DRAIN_DELAY", "0s",
				"NEURODENT_DB_MAX_CONNS", "1",
				"NEURODENT_DB_CONNECT_TIMEOUT", "1s",
				"NEURODENT_DB_STATEMENT_TIMEOUT", "100ms",
				"NEURODENT_DB_MAX_CONN_LIFETIME", "1m",
			),
			want: Config{
				Env:               "staging",
				HTTPAddr:          ":1",
				ReadHeaderTimeout: 1 * time.Second,
				ReadTimeout:       1 * time.Second,
				WriteTimeout:      1001 * time.Millisecond,
				HandlerTimeout:    1 * time.Second,
				IdleTimeout:       10 * time.Second,
				ShutdownTimeout:   1001 * time.Millisecond,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          1,
				DBConnectTimeout:    time.Second,
				DBStatementTimeout:  100 * time.Millisecond,
				CORSAllowedOrigins:  Origins{joined: testOrigins},
				DBMaxConnLifetime:   time.Minute,
			},
		},
		{
			// SHUTDOWN <= 25s и SHUTDOWN >= WRITE, поэтому max для WRITE (1m)
			// в валидном конфиге недостижим — он проверяется в TestLoad_Errors.
			name: "read, idle, drain and db ranges at max, shutdown+drain exactly at budget",
			env: withEnv(
				"NEURODENT_HTTP_ADDR", ":65535",
				"NEURODENT_READ_HEADER_TIMEOUT", "10s",
				"NEURODENT_READ_TIMEOUT", "1m",
				"NEURODENT_IDLE_TIMEOUT", "5m",
				"NEURODENT_DRAIN_DELAY", "10s",
				"NEURODENT_DB_MAX_CONNS", "100",
				"NEURODENT_DB_CONNECT_TIMEOUT", "30s",
				"NEURODENT_DB_MAX_CONN_LIFETIME", "24h",
			),
			want: Config{
				Env:               "staging",
				HTTPAddr:          ":65535",
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       time.Minute,
				WriteTimeout:      15 * time.Second,
				HandlerTimeout:    10 * time.Second,
				IdleTimeout:       5 * time.Minute,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        10 * time.Second,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          100,
				DBConnectTimeout:    30 * time.Second,
				DBStatementTimeout:  5 * time.Second,
				CORSAllowedOrigins:  Origins{joined: testOrigins},
				DBMaxConnLifetime:   24 * time.Hour,
			},
		},
		{
			name: "shutdown at max equals write",
			env: withEnv(
				"NEURODENT_WRITE_TIMEOUT", "25s",
				"NEURODENT_SHUTDOWN_TIMEOUT", "25s",
				"NEURODENT_DRAIN_DELAY", "0s",
			),
			want: Config{
				Env:               "staging",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      25 * time.Second,
				HandlerTimeout:    10 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   25 * time.Second,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,

				DBDSN:               Secret{v: testDSN},
				RateLimitKey:        Secret{v: testRLKey},
				RateLimitAnonPerMin: 300,
				RateLimitUserPerMin: 600,
				DBMaxConns:          10,
				DBConnectTimeout:    5 * time.Second,
				DBStatementTimeout:  5 * time.Second,
				CORSAllowedOrigins:  Origins{joined: testOrigins},
				DBMaxConnLifetime:   30 * time.Minute,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := load(lookupFrom(tt.env))
			if err != nil {
				t.Fatalf("load() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("load() =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantKeys  []string // ровно столько ошибок, по одной на ключ (порядок не важен)
		wantEmpty bool     // хотя бы одна ошибка — errEmpty
	}{
		// ENV
		{
			name:     "missing env",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey},
			wantKeys: []string{"NEURODENT_ENV"},
		},
		{
			name:     "unknown env",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "production"},
			wantKeys: []string{"NEURODENT_ENV"},
		},

		// set-but-empty (N1): пустая строка — ошибка, а не дефолт.
		{
			name:      "empty env",
			env:       map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": ""},
			wantKeys:  []string{"NEURODENT_ENV"},
			wantEmpty: true,
		},
		{
			name:      "empty duration",
			env:       withEnv("NEURODENT_WRITE_TIMEOUT", ""),
			wantKeys:  []string{"NEURODENT_WRITE_TIMEOUT"},
			wantEmpty: true,
		},
		{
			name:      "empty addr",
			env:       withEnv("NEURODENT_HTTP_ADDR", ""),
			wantKeys:  []string{"NEURODENT_HTTP_ADDR"},
			wantEmpty: true,
		},
		{
			name:      "empty log level",
			env:       withEnv("NEURODENT_LOG_LEVEL", ""),
			wantKeys:  []string{"NEURODENT_LOG_LEVEL"},
			wantEmpty: true,
		},

		// Каскад (B1): невалидное поле не порождает ошибок у соседей.
		{
			name: "single typo gives single error",
			env: map[string]string{
				"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey,
				"NEURODENT_ENV": "dev", "NEURODENT_READ_TIMEOUT": "5x",
			},
			wantKeys: []string{"NEURODENT_READ_TIMEOUT"},
		},
		{
			name:     "broken read header timeout does not cascade",
			env:      withEnv("NEURODENT_READ_HEADER_TIMEOUT", "5x"),
			wantKeys: []string{"NEURODENT_READ_HEADER_TIMEOUT"},
		},
		{
			name:     "broken write timeout does not cascade to shutdown",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "abc"),
			wantKeys: []string{"NEURODENT_WRITE_TIMEOUT"},
		},
		{
			name:     "broken drain delay does not cascade to shutdown",
			env:      withEnv("NEURODENT_SHUTDOWN_TIMEOUT", "25s", "NEURODENT_DRAIN_DELAY", "abc"),
			wantKeys: []string{"NEURODENT_DRAIN_DELAY"},
		},
		{
			name:     "broken env does not trigger prod level check",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "prd", "NEURODENT_LOG_LEVEL": "debug"},
			wantKeys: []string{"NEURODENT_ENV"},
		},
		{
			name: "three broken variables give exactly three errors",
			env: withEnv(
				"NEURODENT_READ_TIMEOUT", "5x",
				"NEURODENT_IDLE_TIMEOUT", "forever",
				"NEURODENT_LOG_LEVEL", "loud",
			),
			wantKeys: []string{"NEURODENT_READ_TIMEOUT", "NEURODENT_IDLE_TIMEOUT", "NEURODENT_LOG_LEVEL"},
		},

		// Диапазоны: чуть за границей и абсурдные значения.
		{
			name:     "write timeout 10h",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "10h"),
			wantKeys: []string{"NEURODENT_WRITE_TIMEOUT"},
		},
		{
			name:     "read header timeout just above max",
			env:      withEnv("NEURODENT_READ_HEADER_TIMEOUT", "10001ms"),
			wantKeys: []string{"NEURODENT_READ_HEADER_TIMEOUT"},
		},
		{
			name:     "read timeout just above max",
			env:      withEnv("NEURODENT_READ_TIMEOUT", "60001ms"),
			wantKeys: []string{"NEURODENT_READ_TIMEOUT"},
		},
		{
			name:     "write timeout just above max",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "60001ms"),
			wantKeys: []string{"NEURODENT_WRITE_TIMEOUT"},
		},
		{
			name:     "idle timeout just above max",
			env:      withEnv("NEURODENT_IDLE_TIMEOUT", "300001ms"),
			wantKeys: []string{"NEURODENT_IDLE_TIMEOUT"},
		},
		{
			name:     "shutdown timeout just above max",
			env:      withEnv("NEURODENT_SHUTDOWN_TIMEOUT", "25001ms", "NEURODENT_DRAIN_DELAY", "0s"),
			wantKeys: []string{"NEURODENT_SHUTDOWN_TIMEOUT"},
		},
		{
			name:     "drain delay just above max",
			env:      withEnv("NEURODENT_DRAIN_DELAY", "10001ms"),
			wantKeys: []string{"NEURODENT_DRAIN_DELAY"},
		},
		{
			name:     "read header timeout just below min",
			env:      withEnv("NEURODENT_READ_HEADER_TIMEOUT", "999ms"),
			wantKeys: []string{"NEURODENT_READ_HEADER_TIMEOUT"},
		},
		{
			name:     "idle timeout just below min",
			env:      withEnv("NEURODENT_IDLE_TIMEOUT", "9999ms"),
			wantKeys: []string{"NEURODENT_IDLE_TIMEOUT"},
		},
		{
			name:     "negative drain delay",
			env:      withEnv("NEURODENT_DRAIN_DELAY", "-1ns"),
			wantKeys: []string{"NEURODENT_DRAIN_DELAY"},
		},

		{
			name:     "handler timeout just above max",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "60001ms"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},
		{
			name:     "handler timeout just below min",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "999ms"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},
		{
			name:      "empty handler timeout",
			env:       withEnv("NEURODENT_HANDLER_TIMEOUT", ""),
			wantKeys:  []string{"NEURODENT_HANDLER_TIMEOUT"},
			wantEmpty: true,
		},
		{
			name:     "broken handler timeout does not cascade",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "5x"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},

		// Кросс-полевые инварианты.
		{
			name:     "handler timeout equal to write",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "15s"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},
		{
			name:     "handler timeout at max is in range but not below write",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "1m"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},
		{
			name:     "write at min is in range but not above handler",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "1s", "NEURODENT_HANDLER_TIMEOUT", "1s", "NEURODENT_DB_STATEMENT_TIMEOUT", "500ms"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},
		{
			name:     "read header timeout greater than read timeout",
			env:      withEnv("NEURODENT_READ_HEADER_TIMEOUT", "10s", "NEURODENT_READ_TIMEOUT", "5s"),
			wantKeys: []string{"NEURODENT_READ_HEADER_TIMEOUT"},
		},
		{
			name:     "shutdown less than write",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "20s", "NEURODENT_SHUTDOWN_TIMEOUT", "10s"),
			wantKeys: []string{"NEURODENT_SHUTDOWN_TIMEOUT"},
		},
		{
			name:     "write at max is in range but exceeds shutdown",
			env:      withEnv("NEURODENT_WRITE_TIMEOUT", "1m"),
			wantKeys: []string{"NEURODENT_SHUTDOWN_TIMEOUT"},
		},
		{
			name:     "shutdown plus drain exceeds budget",
			env:      withEnv("NEURODENT_SHUTDOWN_TIMEOUT", "20s", "NEURODENT_DRAIN_DELAY", "5001ms"),
			wantKeys: []string{"NEURODENT_SHUTDOWN_TIMEOUT"},
		},
		{
			name: "shutdown violates both invariants",
			env: withEnv(
				"NEURODENT_WRITE_TIMEOUT", "25s",
				"NEURODENT_SHUTDOWN_TIMEOUT", "20s",
				"NEURODENT_DRAIN_DELAY", "6s",
			),
			wantKeys: []string{"NEURODENT_SHUTDOWN_TIMEOUT", "NEURODENT_SHUTDOWN_TIMEOUT"},
		},
		{
			name:     "prod with debug",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "prod", "NEURODENT_CORS_ALLOWED_ORIGINS": testOrigins, "NEURODENT_LOG_LEVEL": "debug"},
			wantKeys: []string{"NEURODENT_LOG_LEVEL"},
		},

		// База данных.
		{
			name:     "missing dsn",
			env:      map[string]string{"NEURODENT_ENV": "dev", "NEURODENT_RATELIMIT_KEY": testRLKey},
			wantKeys: []string{"NEURODENT_DB_DSN"},
		},
		{
			name:      "empty dsn",
			env:       withEnv("NEURODENT_DB_DSN", ""),
			wantKeys:  []string{"NEURODENT_DB_DSN"},
			wantEmpty: true,
		},
		{
			name:     "missing env, dsn and ratelimit key give three errors",
			env:      map[string]string{},
			wantKeys: []string{"NEURODENT_DB_DSN", "NEURODENT_ENV", "NEURODENT_RATELIMIT_KEY"},
		},
		{
			name:     "max conns zero",
			env:      withEnv("NEURODENT_DB_MAX_CONNS", "0"),
			wantKeys: []string{"NEURODENT_DB_MAX_CONNS"},
		},
		{
			name:     "max conns just above max",
			env:      withEnv("NEURODENT_DB_MAX_CONNS", "101"),
			wantKeys: []string{"NEURODENT_DB_MAX_CONNS"},
		},
		{
			name:     "max conns not a number",
			env:      withEnv("NEURODENT_DB_MAX_CONNS", "ten"),
			wantKeys: []string{"NEURODENT_DB_MAX_CONNS"},
		},
		{
			name:     "max conns overflows int32",
			env:      withEnv("NEURODENT_DB_MAX_CONNS", "4294967306"),
			wantKeys: []string{"NEURODENT_DB_MAX_CONNS"},
		},
		{
			name:      "empty max conns",
			env:       withEnv("NEURODENT_DB_MAX_CONNS", ""),
			wantKeys:  []string{"NEURODENT_DB_MAX_CONNS"},
			wantEmpty: true,
		},
		{
			name:     "connect timeout just above max",
			env:      withEnv("NEURODENT_DB_CONNECT_TIMEOUT", "30001ms"),
			wantKeys: []string{"NEURODENT_DB_CONNECT_TIMEOUT"},
		},
		{
			name:     "statement timeout just below min",
			env:      withEnv("NEURODENT_DB_STATEMENT_TIMEOUT", "99ms"),
			wantKeys: []string{"NEURODENT_DB_STATEMENT_TIMEOUT"},
		},
		{
			name:     "conn lifetime just above max",
			env:      withEnv("NEURODENT_DB_MAX_CONN_LIFETIME", "24h1s"),
			wantKeys: []string{"NEURODENT_DB_MAX_CONN_LIFETIME"},
		},
		{
			name:     "statement timeout equal to handler timeout",
			env:      withEnv("NEURODENT_DB_STATEMENT_TIMEOUT", "10s"),
			wantKeys: []string{"NEURODENT_DB_STATEMENT_TIMEOUT"},
		},
		{
			name:     "statement timeout at max is in range but not below handler",
			env:      withEnv("NEURODENT_DB_STATEMENT_TIMEOUT", "1m"),
			wantKeys: []string{"NEURODENT_DB_STATEMENT_TIMEOUT"},
		},
		{
			name:     "broken handler timeout does not cascade to statement timeout",
			env:      withEnv("NEURODENT_HANDLER_TIMEOUT", "abc", "NEURODENT_DB_STATEMENT_TIMEOUT", "9s"),
			wantKeys: []string{"NEURODENT_HANDLER_TIMEOUT"},
		},

		// CORS (ADR-0015).
		{
			name:     "origins missing in staging",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "staging"},
			wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"},
		},
		{
			name:     "origins missing in prod",
			env:      map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "prod"},
			wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"},
		},
		{
			name:      "origins set but empty",
			env:       withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", ""),
			wantKeys:  []string{"NEURODENT_CORS_ALLOWED_ORIGINS"},
			wantEmpty: true,
		},
		{name: "origin wildcard", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "*"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin null", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "null"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin with path", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://app.example/login"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin with trailing slash", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://app.example/"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin with query", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://app.example?x=1"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin uppercase", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://App.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin default port", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://app.example:443"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin without scheme", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "app.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "origin ftp", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "ftp://app.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "empty element", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://a.example,,https://b.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "duplicate origin", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "https://a.example, https://a.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},
		{name: "http origin outside dev", env: withEnv("NEURODENT_CORS_ALLOWED_ORIGINS", "http://app.example"), wantKeys: []string{"NEURODENT_CORS_ALLOWED_ORIGINS"}},

		// Rate limit и доверенные прокси (ADR-0016).
		{name: "ratelimit key missing", env: map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_ENV": "dev"}, wantKeys: []string{"NEURODENT_RATELIMIT_KEY"}},
		{name: "ratelimit key too short", env: withEnv("NEURODENT_RATELIMIT_KEY", "short"), wantKeys: []string{"NEURODENT_RATELIMIT_KEY"}},
		{name: "ratelimit key empty", env: withEnv("NEURODENT_RATELIMIT_KEY", ""), wantKeys: []string{"NEURODENT_RATELIMIT_KEY"}, wantEmpty: true},
		{name: "anon limit zero", env: withEnv("NEURODENT_RATELIMIT_ANON_PER_MINUTE", "0"), wantKeys: []string{"NEURODENT_RATELIMIT_ANON_PER_MINUTE"}},
		{name: "user limit above max", env: withEnv("NEURODENT_RATELIMIT_USER_PER_MINUTE", "100001"), wantKeys: []string{"NEURODENT_RATELIMIT_USER_PER_MINUTE"}},
		{name: "proxy without mask", env: withEnv("NEURODENT_TRUSTED_PROXIES", "10.0.0.1"), wantKeys: []string{"NEURODENT_TRUSTED_PROXIES"}},
		{name: "proxy with host bits", env: withEnv("NEURODENT_TRUSTED_PROXIES", "10.0.0.1/8"), wantKeys: []string{"NEURODENT_TRUSTED_PROXIES"}},
		{name: "proxy garbage", env: withEnv("NEURODENT_TRUSTED_PROXIES", "10.0.0.0/8,lb"), wantKeys: []string{"NEURODENT_TRUSTED_PROXIES"}},
		{name: "proxies empty", env: withEnv("NEURODENT_TRUSTED_PROXIES", ""), wantKeys: []string{"NEURODENT_TRUSTED_PROXIES"}, wantEmpty: true},

		// Адрес (N2).
		{
			name:     "addr non-numeric port",
			env:      withEnv("NEURODENT_HTTP_ADDR", ":abc"),
			wantKeys: []string{"NEURODENT_HTTP_ADDR"},
		},
		{
			name:     "addr port zero",
			env:      withEnv("NEURODENT_HTTP_ADDR", ":0"),
			wantKeys: []string{"NEURODENT_HTTP_ADDR"},
		},
		{
			name:     "addr port out of range",
			env:      withEnv("NEURODENT_HTTP_ADDR", ":70000"),
			wantKeys: []string{"NEURODENT_HTTP_ADDR"},
		},
		{
			name:     "addr without port",
			env:      withEnv("NEURODENT_HTTP_ADDR", "localhost"),
			wantKeys: []string{"NEURODENT_HTTP_ADDR"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := load(lookupFrom(tt.env))
			if err == nil {
				t.Fatalf("load() error = nil, want errors for %v", tt.wantKeys)
			}
			if got != (Config{}) {
				t.Errorf("load() returned non-zero Config on error: %+v", got)
			}

			gotKeys := errorKeys(t, err)
			want := slices.Sorted(slices.Values(tt.wantKeys))
			if !slices.Equal(gotKeys, want) {
				t.Errorf("error keys = %v, want %v\nfull error:\n%v", gotKeys, want, err)
			}

			if isEmpty := errors.Is(err, errEmpty); isEmpty != tt.wantEmpty {
				t.Errorf("errors.Is(err, errEmpty) = %v, want %v", isEmpty, tt.wantEmpty)
			}
		})
	}
}

// errorKeys раскрывает errors.Join и достаёт имя переменной из каждой ошибки.
// Контракт — префикс "KEY: ", который читает оператор; текст после него
// (сообщения stdlib) не проверяется.
func errorKeys(t *testing.T, err error) []string {
	t.Helper()
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("error %T does not wrap multiple errors: %v", err, err)
	}
	var keys []string
	for _, e := range j.Unwrap() {
		key, _, ok := strings.Cut(e.Error(), ": ")
		if !ok || !strings.HasPrefix(key, "NEURODENT_") {
			t.Fatalf("error %q does not start with a variable name", e)
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestConfig_LogValue(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "dev"}))
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("starting", "config", cfg)

	var rec struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}

	// Ровно этот набор. Новое поле в логе — осознанное решение, которое
	// должно пройти через этот список.
	want := []string{
		"cors_allowed_origins",
		"db_connect_timeout",
		"db_max_conn_lifetime",
		"db_max_conns",
		"db_statement_timeout",
		"drain_delay",
		"env",
		"handler_timeout",
		"http_addr",
		"idle_timeout",
		"log_level",
		"ratelimit_anon_per_minute",
		"ratelimit_user_per_minute",
		"read_header_timeout",
		"read_timeout",
		"shutdown_timeout",
		"trusted_proxies",
		"write_timeout",
	}
	got := slices.Sorted(maps.Keys(rec.Config))
	if !slices.Equal(got, want) {
		t.Errorf("logged keys = %v, want %v", got, want)
	}
	if rec.Config["env"] != "dev" {
		t.Errorf("env = %v, want dev", rec.Config["env"])
	}
}

// FuzzLoad: load не паникует, а успешный результат всегда удовлетворяет
// инвариантам. Инварианты записаны литералами, независимо от config.go.
func FuzzLoad(f *testing.F) {
	f.Add("staging", "5s", "15s", "15s", "10s", "15s", "2s")
	f.Add("dev", "10s", "1m", "1m", "1m", "25s", "0s")
	f.Add("prod", "1s", "1s", "1001ms", "1s", "1001ms", "10s")
	f.Add("staging", "5x", "", "-1s", "", "25s", "10h")

	f.Fuzz(func(t *testing.T, env, rht, rt, wt, ht, st, dd string) {
		cfg, err := load(lookupFrom(map[string]string{
			"NEURODENT_DB_DSN":               testDSN,
			"NEURODENT_RATELIMIT_KEY":        testRLKey,
			"NEURODENT_ENV":                  env,
			"NEURODENT_READ_HEADER_TIMEOUT":  rht,
			"NEURODENT_READ_TIMEOUT":         rt,
			"NEURODENT_WRITE_TIMEOUT":        wt,
			"NEURODENT_HANDLER_TIMEOUT":      ht,
			"NEURODENT_SHUTDOWN_TIMEOUT":     st,
			"NEURODENT_DRAIN_DELAY":          dd,
			"NEURODENT_CORS_ALLOWED_ORIGINS": testOrigins,
		}))
		if err != nil {
			return
		}

		in := func(name string, d, lo, hi time.Duration) {
			if d < lo || d > hi {
				t.Errorf("%s = %s, out of [%s, %s]", name, d, lo, hi)
			}
		}
		in("ReadHeaderTimeout", cfg.ReadHeaderTimeout, time.Second, 10*time.Second)
		in("ReadTimeout", cfg.ReadTimeout, time.Second, time.Minute)
		in("WriteTimeout", cfg.WriteTimeout, time.Second, time.Minute)
		in("HandlerTimeout", cfg.HandlerTimeout, time.Second, time.Minute)
		in("ShutdownTimeout", cfg.ShutdownTimeout, time.Second, 25*time.Second)
		in("DrainDelay", cfg.DrainDelay, 0, 10*time.Second)

		if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
			t.Errorf("ReadHeaderTimeout %s > ReadTimeout %s", cfg.ReadHeaderTimeout, cfg.ReadTimeout)
		}
		if cfg.HandlerTimeout >= cfg.WriteTimeout {
			t.Errorf("HandlerTimeout %s >= WriteTimeout %s", cfg.HandlerTimeout, cfg.WriteTimeout)
		}
		if cfg.DBStatementTimeout >= cfg.HandlerTimeout {
			t.Errorf("DBStatementTimeout %s >= HandlerTimeout %s", cfg.DBStatementTimeout, cfg.HandlerTimeout)
		}
		if cfg.ShutdownTimeout < cfg.WriteTimeout {
			t.Errorf("ShutdownTimeout %s < WriteTimeout %s", cfg.ShutdownTimeout, cfg.WriteTimeout)
		}
		if cfg.ShutdownTimeout+cfg.DrainDelay > 25*time.Second {
			t.Errorf("ShutdownTimeout %s + DrainDelay %s > 25s", cfg.ShutdownTimeout, cfg.DrainDelay)
		}
		if cfg.Env != "dev" && cfg.Env != "staging" && cfg.Env != "prod" {
			t.Errorf("Env = %q", cfg.Env)
		}
	})
}

// TestSecret_NeverPrinted: DSN не должен утечь ни одним стандартным способом
// вывода — ни сам по себе, ни внутри Config.
func TestSecret_NeverPrinted(t *testing.T) {
	cfg, err := load(lookupFrom(withEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBDSN.Reveal() != testDSN {
		t.Fatalf("Reveal() = %q, want the original DSN", cfg.DBDSN.Reveal())
	}

	var outputs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		outputs = append(outputs, fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, cfg.DBDSN))
	}
	js, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(js))

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("cfg", "config", cfg, "dsn", cfg.DBDSN)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "config", cfg, "dsn", cfg.DBDSN)
	outputs = append(outputs, buf.String())

	for _, out := range outputs {
		for _, leak := range []string{"s3cr3t-pw", "db.internal", testDSN, testRLKey} {
			if strings.Contains(out, leak) {
				t.Errorf("secret part %q leaked in output: %s", leak, out)
			}
		}
	}
}

func TestLoadMigrate(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantKeys  []string
		wantEmpty bool
	}{
		{name: "missing both", env: map[string]string{}, wantKeys: []string{"NEURODENT_ENV", "NEURODENT_MIGRATE_DSN"}},
		{name: "empty dsn", env: map[string]string{"NEURODENT_ENV": "dev", "NEURODENT_MIGRATE_DSN": ""}, wantKeys: []string{"NEURODENT_MIGRATE_DSN"}, wantEmpty: true},
		{name: "unknown env", env: map[string]string{"NEURODENT_ENV": "qa", "NEURODENT_MIGRATE_DSN": testDSN}, wantKeys: []string{"NEURODENT_ENV"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadMigrate(lookupFrom(tt.env))
			if got := errorKeys(t, err); !slices.Equal(got, tt.wantKeys) {
				t.Errorf("error keys = %v, want %v", got, tt.wantKeys)
			}
			if errors.Is(err, errEmpty) != tt.wantEmpty {
				t.Errorf("errors.Is(err, errEmpty) = %v, want %v", errors.Is(err, errEmpty), tt.wantEmpty)
			}
		})
	}

	got, err := loadMigrate(lookupFrom(map[string]string{"NEURODENT_ENV": "prod", "NEURODENT_MIGRATE_DSN": testDSN}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Env != "prod" || got.DSN.Reveal() != testDSN {
		t.Errorf("loadMigrate() = %v, %q", got.Env, got.DSN.Reveal())
	}
}

func TestLoad_DevAllowsHTTPOriginsAndEmptyList(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"NEURODENT_DB_DSN":               testDSN,
		"NEURODENT_RATELIMIT_KEY":        testRLKey,
		"NEURODENT_ENV":                  "dev",
		"NEURODENT_CORS_ALLOWED_ORIGINS": "http://localhost:4200, https://app.neurodent.example",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.CORSAllowedOrigins.List(); !slices.Equal(got, []string{"http://localhost:4200", "https://app.neurodent.example"}) {
		t.Errorf("origins = %v", got)
	}
	cfg, err = load(lookupFrom(map[string]string{"NEURODENT_DB_DSN": testDSN, "NEURODENT_RATELIMIT_KEY": testRLKey, "NEURODENT_ENV": "dev"}))
	if err != nil || cfg.CORSAllowedOrigins.List() != nil {
		t.Errorf("dev without origins: %v, %v", cfg.CORSAllowedOrigins.List(), err)
	}
}

func TestLoad_TrustedProxiesAndLimits(t *testing.T) {
	cfg, err := load(lookupFrom(withEnv(
		"NEURODENT_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.10/32,fd00::/8",
		"NEURODENT_RATELIMIT_ANON_PER_MINUTE", "60",
		"NEURODENT_RATELIMIT_USER_PER_MINUTE", "1200",
	)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range cfg.TrustedProxies.List() {
		got = append(got, p.String())
	}
	if !slices.Equal(got, []string{"10.0.0.0/8", "192.168.1.10/32", "fd00::/8"}) {
		t.Errorf("proxies = %v", got)
	}
	if cfg.RateLimitAnonPerMin != 60 || cfg.RateLimitUserPerMin != 1200 {
		t.Errorf("limits = %d/%d", cfg.RateLimitAnonPerMin, cfg.RateLimitUserPerMin)
	}
	if cfg.TrustedProxies.List() == nil && len(got) != 0 {
		t.Error("unreachable")
	}
	cfg, err = load(lookupFrom(withEnv()))
	if err != nil || cfg.TrustedProxies.List() != nil {
		t.Errorf("default proxies = %v, %v; want none (XFF never trusted)", cfg.TrustedProxies.List(), err)
	}
}
