package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
)

// guard — страховка ожидания в select, не синхронизация.
const guard = 5 * time.Second

// syncBuffer — буфер логов, в который можно писать из goroutine хендлера
// (Timeout) и читать из теста без гонки.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records возвращает JSON-записи лога.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range bytes.Lines(b.buf.Bytes()) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newLogger — боевой логгер (с атрибутами из контекста) в буфер.
func newLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return logger.New(buf, slog.LevelDebug, logger.FormatJSON), buf
}

// findRecord возвращает первую запись с данным msg.
func findRecord(t *testing.T, recs []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == msg {
			return r
		}
	}
	t.Fatalf("no log record %q in %v", msg, recs)
	return nil
}

// decodeProblem проверяет Content-Type и декодирует problem-ответ.
func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json; body: %s", got, rec.Body.String())
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a problem: %v", err)
	}
	return p
}

// servePanics вызывает h и возвращает значение паники, вылетевшей наружу.
func servePanics(h http.Handler, w http.ResponseWriter, r *http.Request) (p any) {
	defer func() { p = recover() }()
	h.ServeHTTP(w, r)
	return nil
}

// isAbort сообщает, что паника — http.ErrAbortHandler.
func isAbort(p any) bool {
	err, ok := p.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

// receive ждёт значение из канала со страховкой.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(guard):
		t.Fatalf("timeout waiting for %s", what)
		var zero T
		return zero
	}
}
