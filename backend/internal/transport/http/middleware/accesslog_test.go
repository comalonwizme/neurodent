package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

func TestAccessLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /patients/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "abc")
	})
	mux.HandleFunc("GET /silent", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /early-hints", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusAccepted)
	})

	tests := []struct {
		name       string
		method     string
		target     string
		wantRoute  string
		wantStatus float64
		wantBytes  float64
	}{
		{"pattern, not raw path", http.MethodGet, "/patients/850101300123?iin=850101300123", "GET /patients/{id}", 201, 3},
		{"implicit 200", http.MethodGet, "/silent", "GET /silent", 200, 0},
		{"final status after 1xx", http.MethodGet, "/early-hints", "GET /early-hints", 202, 0},
		{"not found", http.MethodGet, "/nope/850101300123", "unmatched", 404, 19},
		{"method not allowed", http.MethodPost, "/silent", "unmatched", 405, 19},
		{"connect", http.MethodConnect, "/patients/850101300123", "unmatched", 405, 19},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, logs := newLogger()
			h := middleware.Chain(mux, middleware.RequestID, middleware.AccessLog(log, mux))
			req := httptest.NewRequest(tt.method, tt.target, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			r := findRecord(t, logs.records(t), "http request")
			if r["method"] != tt.method || r["route"] != tt.wantRoute || r["status"] != tt.wantStatus {
				t.Errorf("record = %v, want method=%s route=%s status=%v", r, tt.method, tt.wantRoute, tt.wantStatus)
			}
			if r["bytes"] != tt.wantBytes {
				t.Errorf("bytes = %v, want %v", r["bytes"], tt.wantBytes)
			}
			if _, ok := r["duration"]; !ok {
				t.Error("duration missing")
			}
			if r["request_id"] != rec.Header().Get("X-Request-ID") {
				t.Errorf("request_id = %v, want %s", r["request_id"], rec.Header().Get("X-Request-ID"))
			}
			if strings.Contains(logs.String(), "850101300123") {
				t.Errorf("raw path/query (PII) leaked into access log: %s", logs.String())
			}
		})
	}
}

func TestAccessLog_WriterUnwrapsForResponseController(t *testing.T) {
	log, _ := newLogger()
	mux := http.NewServeMux()
	var flushErr error
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	})
	rec := httptest.NewRecorder()
	middleware.AccessLog(log, mux)(mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if flushErr != nil || !rec.Flushed {
		t.Errorf("Flush through wrapper: err=%v flushed=%v", flushErr, rec.Flushed)
	}
}
