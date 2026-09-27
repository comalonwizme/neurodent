package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

const allowedOrigin = "https://app.neurodent.example"

func corsHandler(called *bool) http.Handler {
	return middleware.CORS([]string{allowedOrigin})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORS_ActualRequests(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		allowed bool
	}{
		{"allowed origin", allowedOrigin, true},
		{"foreign origin", "https://evil.example", false},
		{"no origin (non-browser)", "", false},
		{"origin differs only by scheme", "http://app.neurodent.example", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var called bool
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			corsHandler(&called).ServeHTTP(rec, req)

			if !called {
				t.Error("actual request did not reach the handler")
			}
			if !slices.Contains(rec.Header().Values("Vary"), "Origin") {
				t.Error("Vary: Origin missing")
			}
			acao := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.allowed != (acao == tt.origin && acao != "") {
				t.Errorf("Access-Control-Allow-Origin = %q, allowed = %v", acao, tt.allowed)
			}
			if tt.allowed && (rec.Header().Get("Access-Control-Allow-Credentials") != "true" ||
				rec.Header().Get("Access-Control-Expose-Headers") != "X-Request-ID, Retry-After") {
				t.Errorf("credentials/expose headers missing: %v", rec.Header())
			}
		})
	}
}

func TestCORS_Preflight(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		method  string
		headers []string
		want    int
	}{
		{"allowed", allowedOrigin, "POST", []string{"content-type"}, http.StatusNoContent},
		{"allowed, mixed-case headers", allowedOrigin, "DELETE", []string{"Content-Type, X-Request-Id"}, http.StatusNoContent},
		{"allowed, no extra headers", allowedOrigin, "PATCH", nil, http.StatusNoContent},
		{"foreign origin", "https://evil.example", "POST", nil, http.StatusForbidden},
		{"method not allowed", allowedOrigin, "TRACE", nil, http.StatusForbidden},
		{"header not allowed", allowedOrigin, "POST", []string{"content-type, authorization"}, http.StatusForbidden},
		{"header in second line not allowed", allowedOrigin, "POST", []string{"content-type", "x-api-key"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var called bool
			req := httptest.NewRequest(http.MethodOptions, "/", nil)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", tt.method)
			for _, h := range tt.headers {
				req.Header.Add("Access-Control-Request-Headers", h)
			}
			rec := httptest.NewRecorder()
			corsHandler(&called).ServeHTTP(rec, req)

			if called {
				t.Error("preflight reached the handler")
			}
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			acao := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.want == http.StatusForbidden {
				if acao != "" || rec.Header().Get("Content-Type") != "application/problem+json" {
					t.Errorf("rejected preflight: ACAO=%q Content-Type=%q", acao, rec.Header().Get("Content-Type"))
				}
				return
			}
			if acao != tt.origin || rec.Header().Get("Access-Control-Max-Age") != "7200" {
				t.Errorf("allowed preflight headers: %v", rec.Header())
			}
		})
	}
}

func TestCORS_PlainOptionsPassesThrough(t *testing.T) {
	var called bool
	req := httptest.NewRequest(http.MethodOptions, "/", nil) // без Access-Control-Request-Method
	req.Header.Set("Origin", allowedOrigin)
	corsHandler(&called).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("plain OPTIONS must reach the router")
	}
}

func TestCORS_EmptyListDisablesCORS(t *testing.T) {
	h := middleware.CORS(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", allowedOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS headers with an empty origin list")
	}
}

func TestCrossOrigin(t *testing.T) {
	mw, err := middleware.CrossOrigin([]string{allowedOrigin})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		method   string
		site     string // Sec-Fetch-Site
		origin   string
		rejected bool
	}{
		{"trusted origin, same-site POST", http.MethodPost, "same-site", allowedOrigin, false},
		{"sibling subdomain POST (CSRF)", http.MethodPost, "same-site", "https://blog.neurodent.example", true},
		{"cross-site POST", http.MethodPost, "cross-site", "https://evil.example", true},
		{"cross-site DELETE", http.MethodDelete, "cross-site", "https://evil.example", true},
		{"cross-site GET is safe", http.MethodGet, "cross-site", "https://evil.example", false},
		{"same-origin POST", http.MethodPost, "same-origin", "", false},
		{"non-browser POST (no headers)", http.MethodPost, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var called bool
			h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			req := httptest.NewRequest(tt.method, "https://api.neurodent.example/x", nil)
			if tt.site != "" {
				req.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if tt.rejected {
				if called || rec.Code != http.StatusForbidden {
					t.Errorf("not rejected: called=%v status=%d", called, rec.Code)
				}
				if p := decodeProblem(t, rec); p.Code != "forbidden" {
					t.Errorf("problem = %+v", p)
				}
				return
			}
			if !called {
				t.Errorf("rejected unexpectedly: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCrossOrigin_InvalidTrustedOrigin(t *testing.T) {
	if _, err := middleware.CrossOrigin([]string{"not an origin"}); err == nil {
		t.Error("invalid trusted origin accepted")
	}
}
