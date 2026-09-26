package httpx_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

type payload struct {
	Name  string   `json:"name"`
	Age   int      `json:"age"`
	Tags  []string `json:"tags"`
	Inner struct {
		OK bool `json:"ok"`
	} `json:"inner"`
}

// noContentType — маркер кейса без заголовка Content-Type; пустая строка
// в таблице означает application/json.
const noContentType = "-"

func jsonRequest(body, contentType string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != noContentType {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestDecodeJSON_Valid(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
	}{
		{"plain", `{"name":"a","age":3,"tags":["x"],"inner":{"ok":true}}`, "application/json"},
		{"charset param", `{"name":"a","age":3,"tags":["x"],"inner":{"ok":true}}`, "application/json; charset=utf-8"},
		{"surrounding whitespace", "\n\t {\"name\":\"a\",\"age\":3,\"tags\":[\"x\"],\"inner\":{\"ok\":true}} \n", "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got payload
			if err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(tt.body, tt.contentType), &got, 1<<10); err != nil {
				t.Fatalf("DecodeJSON() = %v", err)
			}
			if got.Name != "a" || got.Age != 3 || len(got.Tags) != 1 || !got.Inner.OK {
				t.Errorf("decoded = %+v", got)
			}
		})
	}
}

func TestDecodeJSON_Rejects(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string // "" — application/json
		limit       int64
		wantMsg     string // точное сообщение для клиента
		wantStatus  int
	}{
		{name: "missing content type", body: `{}`, contentType: noContentType, wantMsg: "Content-Type must be application/json"},
		{name: "form content type", body: `{}`, contentType: "application/x-www-form-urlencoded", wantMsg: "Content-Type must be application/json"},
		{name: "text/plain (CSRF simple request)", body: `{}`, contentType: "text/plain", wantMsg: "Content-Type must be application/json"},
		{name: "malformed content type", body: `{}`, contentType: "application/json; =", wantMsg: "Content-Type must be application/json"},
		{name: "empty body", body: ``, wantMsg: "request body must not be empty"},
		{name: "whitespace only", body: " \n\t", wantMsg: "request body must not be empty"},
		{name: "null", body: `null`, wantMsg: "request body must be a JSON object"},
		{name: "array", body: `[{"name":"a"}]`, wantMsg: "request body must be a JSON object"},
		{name: "string", body: `"x"`, wantMsg: "request body must be a JSON object"},
		{name: "garbage after object", body: `{"name":"a"} trailing`, wantMsg: "request body must contain a single JSON object"},
		{name: "two objects", body: `{"name":"a"}{"name":"b"}`, wantMsg: "request body must contain a single JSON object"},
		{name: "extra closing brace", body: `{"name":"a"}}`, wantMsg: "request body must contain a single JSON object"},
		{name: "unknown field", body: `{"name":"a","clinic_id":"x"}`, wantMsg: `unknown field "clinic_id"`},
		{name: "unknown nested field", body: `{"inner":{"ok":true,"admin":true}}`, wantMsg: `unknown field "admin"`},
		{name: "wrong type", body: `{"age":"three"}`, wantMsg: `field "age" must be a number`},
		{name: "wrong nested type", body: `{"inner":{"ok":"yes"}}`, wantMsg: `field "inner.ok" must be a boolean`},
		{name: "wrong string type", body: `{"name":1}`, wantMsg: `field "name" must be a string`},
		{name: "wrong object type", body: `{"inner":1}`, wantMsg: `field "inner" must be an object`},
		{name: "wrong array type", body: `{"tags":"x"}`, wantMsg: `field "tags" must be an array`},
		{name: "syntax error", body: `{"name":}`, wantMsg: "malformed JSON at byte offset 9"},
		{name: "truncated", body: `{"name":"a"`, wantMsg: "malformed JSON: unexpected end of input"},
		{name: "over limit", body: `{"name":"` + strings.Repeat("a", 100) + `"}`, limit: 64, wantMsg: "request body must not exceed 64 bytes", wantStatus: 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ct := tt.contentType
			if ct == "" {
				ct = "application/json"
			}
			limit := tt.limit
			if limit == 0 {
				limit = 1 << 10
			}
			var dst payload
			err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(tt.body, ct), &dst, limit)

			e, ok := errors.AsType[*apperr.Error](err)
			if !ok {
				t.Fatalf("DecodeJSON() = %v, want *apperr.Error", err)
			}
			if e.Kind() != apperr.Invalid {
				t.Errorf("kind = %v, want invalid", e.Kind())
			}
			if e.Message() != tt.wantMsg {
				t.Errorf("message = %q, want %q", e.Message(), tt.wantMsg)
			}
			wantStatus := tt.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusBadRequest
			}
			if got := httpx.StatusOf(err); got != wantStatus {
				t.Errorf("StatusOf() = %d, want %d", got, wantStatus)
			}
		})
	}
}

func TestDecodeJSON_ExactlyAtLimit(t *testing.T) {
	body := `{"name":"abc"}`
	var dst payload
	if err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(body, "application/json"), &dst, int64(len(body))); err != nil {
		t.Fatalf("body of exactly maxBytes rejected: %v", err)
	}
}

func TestDecodeJSON_MessageDoesNotEchoValues(t *testing.T) {
	// Значения из тела (потенциально PHI) не должны попасть в сообщение клиенту.
	const phi = "Иванов-850101300123"
	var dst payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(`{"age":"`+phi+`"}`, "application/json"), &dst, 1<<10)
	e, ok := errors.AsType[*apperr.Error](err)
	if !ok {
		t.Fatalf("DecodeJSON() = %v", err)
	}
	if strings.Contains(e.Message(), phi) {
		t.Errorf("client message echoes body value: %q", e.Message())
	}
}

func TestDecodeJSON_NonPointerIsInternal(t *testing.T) {
	var dst payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(`{}`, "application/json"), dst, 1<<10)
	if apperr.KindOf(err) != apperr.Internal {
		t.Errorf("KindOf() = %v, want internal: non-pointer dst is a programming error", apperr.KindOf(err))
	}
}

func TestDecodeJSON_ReadError(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(iotest.ErrReader(io.ErrClosedPipe)))
	r.Header.Set("Content-Type", "application/json")
	var dst payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), r, &dst, 1<<10)
	e, ok := errors.AsType[*apperr.Error](err)
	if !ok || e.Kind() != apperr.Invalid || e.Message() != "cannot read request body" {
		t.Errorf("DecodeJSON() = %v, want invalid \"cannot read request body\"", err)
	}
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Error("read error cause is not preserved for logs")
	}
}

func TestDecodeJSON_TopLevelTypeMismatch(t *testing.T) {
	// dst не структура: у ошибки типа нет имени поля, сообщаем смещение.
	var dst int
	err := httpx.DecodeJSON(httptest.NewRecorder(), jsonRequest(`{"a":1}`, "application/json"), &dst, 1<<10)
	e, ok := errors.AsType[*apperr.Error](err)
	if !ok || e.Kind() != apperr.Invalid || !strings.HasPrefix(e.Message(), "invalid value at byte offset") {
		t.Errorf("DecodeJSON() = %v", err)
	}
}
