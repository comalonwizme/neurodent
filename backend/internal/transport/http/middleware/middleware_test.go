package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

func TestChain_FirstIsOutermost(t *testing.T) {
	var order []string
	mark := func(name string) middleware.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name+">")
				next.ServeHTTP(w, r)
				order = append(order, "<"+name)
			})
		}
	}
	h := middleware.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mark("a"), mark("b"), mark("c"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"a>", "b>", "c>", "handler", "<c", "<b", "<a"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"uuid kept", "0192f2a4-5b6c-7d8e-9f00-112233445566", true},
		{"min length kept", "abcd1234", true},
		{"max length kept", strings.Repeat("a", 64), true},
		{"dots and underscores kept", "lb.req_42-x", true},
		{"missing generated", "", false},
		{"too short replaced", "abc1234", false},
		{"too long replaced", strings.Repeat("a", 65), false},
		{"space replaced", "abcd 1234", false},
		{"newline replaced (log injection)", "abcd1234\nlevel=ERROR", false},
		{"non-ascii replaced", "абвгдежзий", false},
		{"quote replaced", `abcd"1234`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, logs := newLogger()
			var seen string
			h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = httpx.RequestID(r.Context())
				log.InfoContext(r.Context(), "inside")
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.incoming != "" {
				req.Header.Set("X-Request-ID", tt.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-ID")
			if got != seen {
				t.Errorf("response header %q != context value %q", got, seen)
			}
			if tt.keep && got != tt.incoming {
				t.Errorf("request id = %q, want incoming %q kept", got, tt.incoming)
			}
			if !tt.keep {
				if got == tt.incoming {
					t.Errorf("invalid incoming id %q was accepted", tt.incoming)
				}
				if len(got) != 26 {
					t.Errorf("generated id %q has length %d, want 26", got, len(got))
				}
			}
			if rid := findRecord(t, logs.records(t), "inside")["request_id"]; rid != got {
				t.Errorf("log request_id = %v, want %q", rid, got)
			}
		})
	}
}

func TestRequestID_GeneratedIDsDiffer(t *testing.T) {
	h := middleware.RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	seen := make(map[string]bool)
	for range 100 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		id := rec.Header().Get("X-Request-ID")
		if seen[id] {
			t.Fatalf("duplicate request id %q", id)
		}
		seen[id] = true
	}
}

func TestSecurityHeaders(t *testing.T) {
	base := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	}
	for _, hsts := range []bool{false, true} {
		t.Run(map[bool]string{false: "dev", true: "non-dev"}[hsts], func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			middleware.SecurityHeaders(hsts)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
				ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			for k, v := range base {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			got := rec.Header().Get("Strict-Transport-Security")
			if hsts && got != "max-age=63072000; includeSubDomains" {
				t.Errorf("HSTS = %q, want two years with includeSubDomains", got)
			}
			if !hsts && got != "" {
				t.Errorf("HSTS = %q in dev, want absent", got)
			}
		})
	}
}

func TestSecurityHeaders_HandlerCanOverrideCacheControl(t *testing.T) {
	rec := httptest.NewRecorder()
	middleware.SecurityHeaders(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, handler override lost", got)
	}
}
