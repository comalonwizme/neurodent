package middleware_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

func TestBodyLimit_ContentLengthOverLimitRejectedBeforeHandler(t *testing.T) {
	called := false
	h := middleware.BodyLimit(10)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 11))))

	if called {
		t.Error("handler called despite Content-Length over limit")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Code != "payload_too_large" || p.Detail != "request body must not exceed 10 bytes" {
		t.Errorf("problem = %+v", p)
	}
}

func TestBodyLimit_StreamingBodyCutAtLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"exactly at limit", 10, false},
		{"one byte over", 11, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var readErr error
			h := middleware.BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, readErr = io.ReadAll(r.Body)
				if readErr != nil {
					httpx.WriteError(w, r, slog.New(slog.DiscardHandler), readErr)
				}
			}))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", tt.size)))
			req.ContentLength = -1 // chunked: длина заранее неизвестна
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			_, isMaxBytes := errors.AsType[*http.MaxBytesError](readErr)
			if isMaxBytes != tt.wantErr {
				t.Errorf("read error = %v, want MaxBytesError: %v", readErr, tt.wantErr)
			}
			if tt.wantErr && rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413", rec.Code)
			}
		})
	}
}
