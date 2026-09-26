package config // white-box: нужна неэкспортируемая load и errEmpty

import (
	"bytes"
	"encoding/json"
	"errors"
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

// withEnv — окружение staging плюс переопределения. Staging выбран базой,
// потому что у него ненулевой DrainDelay и все инварианты работают.
func withEnv(kv ...string) map[string]string {
	m := map[string]string{"NEURODENT_ENV": "staging"}
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
			env:  map[string]string{"NEURODENT_ENV": "dev"},
			want: Config{
				Env:               "dev",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,
			},
		},
		{
			name: "staging on defaults drains for 2s",
			env:  map[string]string{"NEURODENT_ENV": "staging"},
			want: Config{
				Env:               "staging",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        2 * time.Second,
				LogLevel:          slog.LevelInfo,
			},
		},
		{
			name: "prod with info level",
			env:  map[string]string{"NEURODENT_ENV": "prod", "NEURODENT_LOG_LEVEL": "info"},
			want: Config{
				Env:               "prod",
				HTTPAddr:          "127.0.0.1:8080",
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        2 * time.Second,
				LogLevel:          slog.LevelInfo,
			},
		},
		{
			name: "dev with every variable customized",
			env: map[string]string{
				"NEURODENT_ENV":                 "dev",
				"NEURODENT_HTTP_ADDR":           "0.0.0.0:9090",
				"NEURODENT_READ_HEADER_TIMEOUT": "2s",
				"NEURODENT_READ_TIMEOUT":        "30s",
				"NEURODENT_WRITE_TIMEOUT":       "20s",
				"NEURODENT_IDLE_TIMEOUT":        "90s",
				"NEURODENT_SHUTDOWN_TIMEOUT":    "20s",
				"NEURODENT_DRAIN_DELAY":         "3s",
				"NEURODENT_LOG_LEVEL":           "debug",
			},
			want: Config{
				Env:               "dev",
				HTTPAddr:          "0.0.0.0:9090",
				ReadHeaderTimeout: 2 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      20 * time.Second,
				IdleTimeout:       90 * time.Second,
				ShutdownTimeout:   20 * time.Second,
				DrainDelay:        3 * time.Second,
				LogLevel:          slog.LevelDebug,
			},
		},
		{
			name: "all ranges at min",
			env: withEnv(
				"NEURODENT_HTTP_ADDR", ":1",
				"NEURODENT_READ_HEADER_TIMEOUT", "1s",
				"NEURODENT_READ_TIMEOUT", "1s",
				"NEURODENT_WRITE_TIMEOUT", "1s",
				"NEURODENT_IDLE_TIMEOUT", "10s",
				"NEURODENT_SHUTDOWN_TIMEOUT", "1s",
				"NEURODENT_DRAIN_DELAY", "0s",
			),
			want: Config{
				Env:               "staging",
				HTTPAddr:          ":1",
				ReadHeaderTimeout: 1 * time.Second,
				ReadTimeout:       1 * time.Second,
				WriteTimeout:      1 * time.Second,
				IdleTimeout:       10 * time.Second,
				ShutdownTimeout:   1 * time.Second,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,
			},
		},
		{
			// SHUTDOWN <= 25s и SHUTDOWN >= WRITE, поэтому max для WRITE (1m)
			// в валидном конфиге недостижим — он проверяется в TestLoad_Errors.
			name: "read, idle and drain at max, shutdown+drain exactly at budget",
			env: withEnv(
				"NEURODENT_HTTP_ADDR", ":65535",
				"NEURODENT_READ_HEADER_TIMEOUT", "10s",
				"NEURODENT_READ_TIMEOUT", "1m",
				"NEURODENT_IDLE_TIMEOUT", "5m",
				"NEURODENT_DRAIN_DELAY", "10s",
			),
			want: Config{
				Env:               "staging",
				HTTPAddr:          ":65535",
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       time.Minute,
				WriteTimeout:      15 * time.Second,
				IdleTimeout:       5 * time.Minute,
				ShutdownTimeout:   15 * time.Second,
				DrainDelay:        10 * time.Second,
				LogLevel:          slog.LevelInfo,
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
				IdleTimeout:       120 * time.Second,
				ShutdownTimeout:   25 * time.Second,
				DrainDelay:        0,
				LogLevel:          slog.LevelInfo,
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
			env:      map[string]string{},
			wantKeys: []string{"NEURODENT_ENV"},
		},
		{
			name:     "unknown env",
			env:      map[string]string{"NEURODENT_ENV": "production"},
			wantKeys: []string{"NEURODENT_ENV"},
		},

		// set-but-empty (N1): пустая строка — ошибка, а не дефолт.
		{
			name:      "empty env",
			env:       map[string]string{"NEURODENT_ENV": ""},
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
			env:      map[string]string{"NEURODENT_ENV": "prd", "NEURODENT_LOG_LEVEL": "debug"},
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

		// Кросс-полевые инварианты.
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
			env:      map[string]string{"NEURODENT_ENV": "prod", "NEURODENT_LOG_LEVEL": "debug"},
			wantKeys: []string{"NEURODENT_LOG_LEVEL"},
		},

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
	cfg, err := load(lookupFrom(map[string]string{"NEURODENT_ENV": "dev"}))
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
		"drain_delay",
		"env",
		"http_addr",
		"idle_timeout",
		"log_level",
		"read_header_timeout",
		"read_timeout",
		"shutdown_timeout",
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
	f.Add("staging", "5s", "15s", "15s", "15s", "2s")
	f.Add("dev", "10s", "1m", "1m", "25s", "0s")
	f.Add("prod", "1s", "1s", "1s", "1s", "10s")
	f.Add("staging", "5x", "", "-1s", "25s", "10h")

	f.Fuzz(func(t *testing.T, env, rht, rt, wt, st, dd string) {
		cfg, err := load(lookupFrom(map[string]string{
			"NEURODENT_ENV":                 env,
			"NEURODENT_READ_HEADER_TIMEOUT": rht,
			"NEURODENT_READ_TIMEOUT":        rt,
			"NEURODENT_WRITE_TIMEOUT":       wt,
			"NEURODENT_SHUTDOWN_TIMEOUT":    st,
			"NEURODENT_DRAIN_DELAY":         dd,
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
		in("ShutdownTimeout", cfg.ShutdownTimeout, time.Second, 25*time.Second)
		in("DrainDelay", cfg.DrainDelay, 0, 10*time.Second)

		if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
			t.Errorf("ReadHeaderTimeout %s > ReadTimeout %s", cfg.ReadHeaderTimeout, cfg.ReadTimeout)
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
