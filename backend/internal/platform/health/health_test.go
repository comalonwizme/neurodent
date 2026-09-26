package health_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
	ok       = response{http.StatusOK, `{"status":"ok"}`}
	draining = response{http.StatusServiceUnavailable, `{"status":"draining"}`}
)

func TestLiveness(t *testing.T) {
	p := health.NewProbe()
	check(t, call(p.Liveness, "/healthz"), ok)

	// Liveness не зависит от drain: иначе оркестратор убил бы под,
	// который честно дорабатывает запросы.
	p.StartDraining()
	check(t, call(p.Liveness, "/healthz"), ok)
}

func TestReadiness(t *testing.T) {
	p := health.NewProbe()
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

	p := health.NewProbe()
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
