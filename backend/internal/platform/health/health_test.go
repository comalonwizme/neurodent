package health_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
)

type response struct {
	code int
	body string
}

func call(h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// check проверяет всё, что обещает контракт probe-эндпоинта.
func check(t *testing.T, rec *httptest.ResponseRecorder, want response) {
	t.Helper()
	if rec.Code != want.code {
		t.Errorf("status = %d, want %d", rec.Code, want.code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Body.String(); got != want.body {
		t.Errorf("body = %q, want %q", got, want.body)
	}
}

var (
	ok          = response{http.StatusOK, `{"status":"ok"}`}
	draining    = response{http.StatusServiceUnavailable, `{"status":"draining"}`}
	unavailable = response{http.StatusServiceUnavailable, `{"status":"unavailable"}`}
)

func TestLiveness(t *testing.T) {
	p := health.NewProbe(slog.New(slog.DiscardHandler))
	check(t, call(p.Liveness, "/healthz"), ok)

	// Liveness не зависит от drain: иначе оркестратор убил бы под,
	// который честно дорабатывает запросы.
	p.StartDraining()
	check(t, call(p.Liveness, "/healthz"), ok)
}

func TestReadiness(t *testing.T) {
	p := health.NewProbe(slog.New(slog.DiscardHandler))
	check(t, call(p.Readiness, "/readyz"), ok)

	p.StartDraining()
	check(t, call(p.Readiness, "/readyz"), draining)

	// Drain необратим, повторный вызов ничего не ломает.
	p.StartDraining()
	check(t, call(p.Readiness, "/readyz"), draining)
}

// TestReadiness_ConcurrentDrain воспроизводит реальную картину: goroutine
// остановки пишет флаг, goroutine запросов его читают. Основной ассерт
// делает race detector (go test -race).
func TestReadiness_ConcurrentDrain(t *testing.T) {
	const readers = 32
	const callsPerReader = 50

	p := health.NewProbe(slog.New(slog.DiscardHandler))
	start := make(chan struct{})
	codes := make([][]int, readers)

	var wg sync.WaitGroup
	for i := range readers {
		codes[i] = make([]int, 0, callsPerReader)
		wg.Go(func() {
			<-start
			for range callsPerReader {
				codes[i] = append(codes[i], call(p.Readiness, "/readyz").Code)
			}
		})
	}
	wg.Go(func() {
		<-start
		p.StartDraining()
	})
	close(start)
	wg.Wait()

	// Проверки — только в goroutine теста.
	for i, cs := range codes {
		sawDraining := false
		for _, c := range cs {
			switch c {
			case http.StatusOK:
				if sawDraining {
					t.Fatalf("reader %d: got 200 after 503, drain must be monotonic", i)
				}
			case http.StatusServiceUnavailable:
				sawDraining = true
			default:
				t.Fatalf("reader %d: unexpected status %d", i, c)
			}
		}
	}
	check(t, call(p.Readiness, "/readyz"), draining)
}

func TestReadiness_Checks(t *testing.T) {
	failing := health.Check{Name: "postgres", Fn: func(context.Context) error {
		return errors.New("connection refused to 10.0.0.5:5432")
	}}
	passing := health.Check{Name: "cache", Fn: func(context.Context) error { return nil }}

	tests := []struct {
		name   string
		checks []health.Check
		want   response
		logged bool
	}{
		{"no checks", nil, ok, false},
		{"all pass", []health.Check{passing, passing}, ok, false},
		{"one fails", []health.Check{passing, failing}, unavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			p := health.NewProbe(slog.New(slog.NewJSONHandler(&logs, nil)), tt.checks...)
			rec := call(p.Readiness, "/readyz")
			check(t, rec, tt.want)
			// Детали инфраструктуры — в лог, не в ответ.
			if strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Errorf("response leaks check error: %s", rec.Body.String())
			}
			if got := strings.Contains(logs.String(), `"check":"postgres"`); got != tt.logged {
				t.Errorf("failed check logged = %v, want %v: %s", got, tt.logged, logs.String())
			}
		})
	}
}

func TestReadiness_CheckHasShortDeadline(t *testing.T) {
	var budget time.Duration
	slow := health.Check{Name: "slow", Fn: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("no deadline")
		}
		budget = time.Until(deadline)
		<-ctx.Done() // зависшая зависимость: выходим только по таймауту
		return ctx.Err()
	}}
	p := health.NewProbe(slog.New(slog.DiscardHandler), slow)
	check(t, call(p.Readiness, "/readyz"), unavailable)
	if budget <= 0 || budget > 500*time.Millisecond {
		t.Errorf("check budget = %s, want (0, 500ms]: must answer before kubelet's 1s probe timeout", budget)
	}
}

func TestProbe_DrainAndLivenessSkipChecks(t *testing.T) {
	calls := 0
	counting := health.Check{Name: "c", Fn: func(context.Context) error { calls++; return nil }}
	p := health.NewProbe(slog.New(slog.DiscardHandler), counting)

	check(t, call(p.Liveness, "/healthz"), ok)
	if calls != 0 {
		t.Errorf("liveness ran %d dependency checks, want 0", calls)
	}
	p.StartDraining()
	check(t, call(p.Readiness, "/readyz"), draining)
	if calls != 0 {
		t.Errorf("readiness in drain ran %d checks, want 0", calls)
	}
}
