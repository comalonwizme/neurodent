package app // white-box: newRouter и registerRoutes намеренно не экспортируются

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range bytes.Lines(b.buf.Bytes()) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		out = append(out, rec)
	}
	return out
}

// Таймауты хендлера в тестах. Короткий — только там, где таймаут должен
// сработать: под -race и параллельными тестами чтение 1 МиБ может занять
// больше 20ms, и с общим коротким таймаутом тест на 413 иногда ловил 503.
const (
	timeoutShort = 20 * time.Millisecond
	timeoutLong  = 5 * time.Second
)

// testRouter — боевая цепочка и боевые маршруты плюс тестовые хендлеры,
// которые паникуют, зависают и читают тело.
func testRouter(t *testing.T, handlerTimeout time.Duration) (http.Handler, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	log := logger.New(logs, slog.LevelDebug, logger.FormatJSON)

	mux := http.NewServeMux()
	registerRoutes(mux, log, health.NewProbe(log))
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	mux.HandleFunc("GET /partial-panic", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"partial":`)
		panic("boom")
	})
	mux.HandleFunc("GET /slow", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		// Лимит декодера больше общего: 413 обязан прийти от middleware.
		if err := httpx.DecodeJSON(w, r, &v, 10*maxBodyBytes); err != nil {
			httpx.WriteError(w, r, log, err)
			return
		}
		_ = httpx.WriteJSON(w, http.StatusOK, v)
	})

	return newRouter(mux, log, routerOptions{handlerTimeout: handlerTimeout, hsts: true}), logs
}

var securityHeaders = map[string]string{
	"X-Content-Type-Options":    "nosniff",
	"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
	"Referrer-Policy":           "no-referrer",
	"Cache-Control":             "no-store",
	"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
}

// TestRouter_ErrorResponsesAreUniform проверяет порядок цепочки через его
// наблюдаемые следствия: ошибка, где бы она ни родилась (роутер, BodyLimit,
// Timeout, Recover), даёт problem-ответ с request_id и security-заголовками
// и попадает в access-лог с итоговым статусом.
func TestRouter_ErrorResponsesAreUniform(t *testing.T) {
	bigBody := strings.Repeat("a", maxBodyBytes+1)
	tests := []struct {
		name    string
		req     func() *http.Request
		status  int
		code    string
		route   string
		timeout time.Duration // 0 — timeoutLong
	}{
		{
			name:   "router 404",
			req:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/nope", nil) },
			status: 404, code: "not_found", route: "unmatched",
		},
		{
			name:   "router 405",
			req:    func() *http.Request { return httptest.NewRequest(http.MethodPost, "/healthz", nil) },
			status: 405, code: "method_not_allowed", route: "unmatched",
		},
		{
			name: "body limit by Content-Length",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(bigBody))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
			status: 413, code: "payload_too_large", route: "POST /echo",
		},
		{
			name: "body limit on chunked body",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"a":"`+bigBody+`"}`))
				r.Header.Set("Content-Type", "application/json")
				r.ContentLength = -1
				return r
			},
			status: 413, code: "payload_too_large", route: "POST /echo",
		},
		{
			name:   "handler timeout",
			req:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/slow", nil) },
			status: 503, code: "unavailable", route: "GET /slow", timeout: timeoutShort,
		},
		{
			name:   "panic",
			req:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/panic", nil) },
			status: 500, code: "internal", route: "GET /panic",
		},
		{
			// Timeout внутри Recover буферизует ответ: частичная запись не
			// уходит клиенту, и паника даёт чистый 500, а не обрыв.
			name:   "panic after partial write",
			req:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/partial-panic", nil) },
			status: 500, code: "internal", route: "GET /partial-panic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			timeout := tt.timeout
			if timeout == 0 {
				timeout = timeoutLong
			}
			h, logs := testRouter(t, timeout)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tt.req())

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type = %q; body: %s", ct, rec.Body.String())
			}
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			id := rec.Header().Get("X-Request-ID")
			if p.Code != tt.code || p.Status != tt.status || id == "" || p.RequestID != id {
				t.Errorf("problem = %+v, X-Request-ID = %q", p, id)
			}
			for k, v := range securityHeaders {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}

			var access map[string]any
			for _, r := range logs.records(t) {
				if r["msg"] == "http request" {
					access = r
				}
			}
			if access == nil {
				t.Fatal("no access log record")
			}
			if access["status"] != float64(tt.status) || access["route"] != tt.route || access["request_id"] != id {
				t.Errorf("access log = %v, want status=%d route=%s request_id=%s", access, tt.status, tt.route, id)
			}
		})
	}
}

func TestRouter_MethodNotAllowedListsAllowed(t *testing.T) {
	h, _ := testRouter(t, timeoutLong)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/readyz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want \"GET, HEAD\"", got)
	}
}

func TestRouter_Healthz(t *testing.T) {
	h, _ := testRouter(t, timeoutLong)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID missing on success response")
	}
	for k, v := range securityHeaders {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// TestRouter_Head — на реальном сервере: подавление тела для HEAD делает
// net/http, рекордер его не эмулирует.
func TestRouter_Head(t *testing.T) {
	h, _ := testRouter(t, timeoutLong)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/healthz", "/readyz"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD %s status = %d, want 200", path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("HEAD %s returned body %q", path, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("HEAD %s Content-Type = %q, want the same as GET", path, ct)
		}
	}
}

func TestRouter_NoHSTSInDev(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	registerRoutes(mux, log, health.NewProbe(log))
	h := newRouter(mux, log, routerOptions{handlerTimeout: time.Second, hsts: false})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q in dev", got)
	}
}

func TestRouter_CleanPathRedirectPreserved(t *testing.T) {
	// Путь без маршрута, но требующий очистки: ServeMux отвечает редиректом,
	// и routeProblems не должен превратить его в 404.
	h, _ := testRouter(t, timeoutLong)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/../nope", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/nope" {
		t.Errorf("got %d Location=%q, want 307 to /nope", rec.Code, rec.Header().Get("Location"))
	}
}
