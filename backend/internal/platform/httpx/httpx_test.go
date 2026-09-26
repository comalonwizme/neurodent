package httpx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/gen/platformapi"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

func TestRequestID_RoundTrip(t *testing.T) {
	ctx := t.Context()
	if got := httpx.RequestID(ctx); got != "" {
		t.Errorf("RequestID(empty ctx) = %q, want empty", got)
	}
	ctx = httpx.WithRequestID(ctx, "abc")
	if got := httpx.RequestID(ctx); got != "abc" {
		t.Errorf("RequestID() = %q, want abc", got)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := httpx.WriteJSON(rec, http.StatusCreated, map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got, want := rec.Header().Get("Content-Length"), fmt.Sprint(rec.Body.Len()); got != want {
		t.Errorf("Content-Length = %s, body is %s bytes", got, want)
	}
	if got := rec.Body.String(); got != "{\"n\":1}\n" {
		t.Errorf("body = %q", got)
	}
}

func TestWriteJSON_EncodeErrorWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	err := httpx.WriteJSON(rec, http.StatusOK, map[string]any{"ch": make(chan int)})
	if err == nil {
		t.Fatal("WriteJSON() = nil, want encode error")
	}
	// Ответ не начат: вызывающий может отдать нормальную ошибку.
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("response started despite encode error: headers=%v body=%q", rec.Header(), rec.Body.String())
	}
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

func TestWriteJSON_WriteErrorReturned(t *testing.T) {
	err := httpx.WriteJSON(failingWriter{httptest.NewRecorder()}, http.StatusOK, 1)
	if err == nil || !strings.Contains(err.Error(), "client gone") {
		t.Errorf("WriteJSON() = %v, want wrapped write error", err)
	}
}

func TestStatusOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"invalid", apperr.New(apperr.Invalid, ""), 400},
		{"unauthenticated", apperr.New(apperr.Unauthenticated, ""), 401},
		{"forbidden", apperr.New(apperr.Forbidden, ""), 403},
		{"not found", apperr.New(apperr.NotFound, ""), 404},
		{"conflict", apperr.New(apperr.Conflict, ""), 409},
		{"rate limited", apperr.New(apperr.RateLimited, ""), 429},
		{"internal", apperr.New(apperr.Internal, ""), 500},
		{"unavailable", apperr.New(apperr.Unavailable, ""), 503},
		{"unknown kind", apperr.New(apperr.Kind(99), ""), 500},
		{"plain error", errors.New("boom"), 500},
		{"wrapped apperr", fmt.Errorf("svc: %w", apperr.New(apperr.NotFound, "")), 404},
		{"max bytes", &http.MaxBytesError{Limit: 10}, 413},
		{"max bytes inside invalid", apperr.Wrap(apperr.Invalid, "too big", &http.MaxBytesError{Limit: 10}), 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := httpx.StatusOf(tt.err); got != tt.want {
				t.Errorf("StatusOf() = %d, want %d", got, tt.want)
			}
		})
	}
}

// problemOf проверяет заголовки problem-ответа и декодирует тело.
func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var p httpx.Problem
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("body is not a Problem: %v", err)
	}
	return p
}

func requestWithID(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	return r.WithContext(httpx.WithRequestID(r.Context(), id))
}

func TestWriteError_ClientError(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil)) // info: 4xx не пишутся
	rec := httptest.NewRecorder()

	err := apperr.Wrap(apperr.NotFound, "patient not found", errors.New("no rows in result set"))
	httpx.WriteError(rec, requestWithID("req-1"), log, err)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	want := httpx.Problem{
		Type: "about:blank", Title: "Not Found", Status: 404,
		Detail: "patient not found", Code: "not_found", RequestID: "req-1",
	}
	if got := problemOf(t, rec); got != want {
		t.Errorf("problem = %+v, want %+v", got, want)
	}
	if logs.Len() != 0 {
		t.Errorf("4xx logged at info level: %s", logs.String())
	}
}

func TestWriteError_ServerErrorHidesDetails(t *testing.T) {
	const secret = "relation clinic.patients does not exist"
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{
			name:   "plain error",
			err:    errors.New(secret),
			status: 500, code: "internal", detail: "internal error",
		},
		{
			name:   "internal apperr with message",
			err:    apperr.Wrap(apperr.Internal, "db table missing", errors.New(secret)),
			status: 500, code: "internal", detail: "internal error",
		},
		{
			name:   "unavailable apperr with message",
			err:    apperr.Wrap(apperr.Unavailable, "postgres pool exhausted", errors.New(secret)),
			status: 503, code: "unavailable", detail: "service temporarily unavailable, retry later",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			rec := httptest.NewRecorder()

			httpx.WriteError(rec, requestWithID("req-2"), log, tt.err)

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			raw := rec.Body.String()
			p := problemOf(t, rec)
			if p.Code != tt.code || p.Detail != tt.detail || p.RequestID != "req-2" {
				t.Errorf("problem = %+v", p)
			}
			for _, leak := range []string{secret, "db table", "postgres"} {
				if strings.Contains(raw, leak) {
					t.Errorf("5xx body leaks %q: %s", leak, raw)
				}
			}
			// Причина обязана попасть в лог: иначе 5xx не расследовать.
			if !strings.Contains(logs.String(), secret) {
				t.Errorf("5xx cause not logged: %s", logs.String())
			}
		})
	}
}

func TestWriteError_PayloadTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	err := apperr.Wrap(apperr.Invalid, "request body must not exceed 10 bytes", &http.MaxBytesError{Limit: 10})
	httpx.WriteError(rec, requestWithID("r"), slog.New(slog.DiscardHandler), err)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
	if p := problemOf(t, rec); p.Code != "payload_too_large" || p.Detail != "request body must not exceed 10 bytes" {
		t.Errorf("problem = %+v", p)
	}
}

func TestWriteProblem_DropsHandlerCacheHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "public, max-age=3600")
	rec.Header().Set("ETag", `"v1"`)
	httpx.WriteProblem(rec, requestWithID(""), http.StatusConflict, "conflict", "version mismatch")

	if got := rec.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q leaked into error response", got)
	}
	p := problemOf(t, rec)
	if p.RequestID != "" {
		t.Errorf("request_id = %q, want omitted", p.RequestID)
	}
}

func TestWriteProblem_WithoutRequestIDOmitsField(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background())
	httpx.WriteProblem(rec, r, http.StatusNotFound, "not_found", "")
	if strings.Contains(rec.Body.String(), "request_id") || strings.Contains(rec.Body.String(), "detail") {
		t.Errorf("empty optional fields must be omitted: %s", rec.Body.String())
	}
}

// Ошибки параметров — те самые типы, что генерирует oapi-codegen.
func TestParamErrorHandler(t *testing.T) {
	const secret = "Иванов-850101300123"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"invalid format", &platformapi.InvalidParamFormatError{ParamName: "id", Err: errors.New("invalid UUID '" + secret + "'")}, `parameter "id" is invalid`},
		{"required", &platformapi.RequiredParamError{ParamName: "clinic_id"}, `parameter "clinic_id" is required`},
		{"required header", &platformapi.RequiredHeaderError{ParamName: "X-Clinic", Err: errors.New("missing")}, `parameter "X-Clinic" is required`},
		{"wrapped", fmt.Errorf("bind: %w", &platformapi.UnmarshalingParamError{ParamName: "filter", Err: errors.New(secret)}), `parameter "filter" is invalid`},
		{"unknown error", errors.New("boom " + secret), "request parameters are invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			httpx.ParamErrorHandler(slog.New(slog.DiscardHandler))(rec, requestWithID("r1"), tt.err)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			p := problemOf(t, rec)
			if p.Detail != tt.want || p.Code != "invalid" || p.RequestID != "r1" {
				t.Errorf("problem = %+v, want detail %q", p, tt.want)
			}
			if strings.Contains(rec.Body.String(), "850101300123") {
				t.Errorf("parameter value echoed to the client: %s", rec.Body.String())
			}
		})
	}
}
