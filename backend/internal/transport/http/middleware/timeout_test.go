package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

// short — таймаут для сценариев, где он должен сработать. Тест не спит:
// хендлер ждёт отмены контекста, так что прогон занимает ровно short.
const short = 20 * time.Millisecond

func TestTimeout_FastHandlerResponsePassesThrough(t *testing.T) {
	log, _ := newLogger()
	h := middleware.Timeout(guard, log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("handler context has no deadline")
		}
		if got := w.Header().Get("X-Outer"); got != "1" {
			t.Errorf("handler does not see outer header: %q", got)
		}
		w.Header().Set("X-Handler", "yes")
		w.Header().Del("X-Remove-Me")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "created")
	}))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Outer", "1")
	rec.Header().Set("X-Remove-Me", "1")
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusCreated || rec.Body.String() != "created" {
		t.Errorf("got %d %q, want 201 created", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Handler") != "yes" || rec.Header().Get("X-Outer") != "1" {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Header().Get("X-Remove-Me") != "" {
		t.Error("header deleted by handler is still present")
	}
}

func TestTimeout_EmptyResponseAndDuplicateWriteHeader(t *testing.T) {
	log, _ := newLogger()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"writes nothing", func(http.ResponseWriter, *http.Request) {}, http.StatusOK},
		{"first WriteHeader wins", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusEarlyHints) // 1xx не финальный
			w.WriteHeader(http.StatusAccepted)
			w.WriteHeader(http.StatusTeapot)
		}, http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			middleware.Timeout(guard, log)(tt.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != tt.want || rec.Body.Len() != 0 {
				t.Errorf("got %d %q, want %d empty", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestTimeout_ImplicitStatus200(t *testing.T) {
	log, _ := newLogger()
	h := middleware.Timeout(guard, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestTimeout_SlowHandlerGets503AndLateWritesFail(t *testing.T) {
	log, logs := newLogger()
	ctxErr := make(chan error, 1)
	writeErr := make(chan error, 1)
	proceed := make(chan struct{})
	h := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial") // до таймаута: уйдёт в буфер и будет выброшено
		<-r.Context().Done()
		ctxErr <- r.Context().Err()
		<-proceed // тест отпускает хендлер, когда 503 уже записан
		_, err := io.WriteString(w, "late")
		writeErr <- err
	}), middleware.RequestID, middleware.Timeout(short, log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	close(proceed)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	p := decodeProblem(t, rec)
	if p.Code != "unavailable" || p.RequestID == "" {
		t.Errorf("problem = %+v", p)
	}
	if strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("buffered handler output leaked into 503: %s", rec.Body.String())
	}
	if err := receive(t, ctxErr, "handler ctx error"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("handler ctx error = %v, want DeadlineExceeded", err)
	}
	if err := receive(t, writeErr, "late write result"); !errors.Is(err, http.ErrHandlerTimeout) {
		t.Errorf("late write error = %v, want http.ErrHandlerTimeout", err)
	}
	r := findRecord(t, logs.records(t), "handler timed out")
	if r["request_id"] != p.RequestID {
		t.Errorf("timeout log request_id = %v, want %s", r["request_id"], p.RequestID)
	}
}

func TestTimeout_ClientGoneWritesNothing(t *testing.T) {
	log, _ := newLogger()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := middleware.Timeout(guard, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // клиент ушёл
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("wrote a response to a gone client: %d %q", rec.Code, rec.Body.String())
	}
}

func TestTimeout_PanicReachesRecoverWithOriginalStack(t *testing.T) {
	log, logs := newLogger()
	h := middleware.Chain(http.HandlerFunc(panickingHandler),
		middleware.Recover(log), middleware.Timeout(guard, log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	decodeProblem(t, rec)
	r := findRecord(t, logs.records(t), "panic recovered")
	if stack, _ := r["stack"].(string); !strings.Contains(stack, "panickingHandler") {
		t.Errorf("stack is not from the handler goroutine:\n%s", stack)
	}
}

func TestTimeout_PartialWriteThenPanicStillClean500(t *testing.T) {
	log, _ := newLogger()
	h := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"partial":`)
		panic("boom")
	}), middleware.Recover(log), middleware.Timeout(guard, log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: buffered partial write must not reach the client", rec.Code)
	}
	decodeProblem(t, rec)
}

func TestTimeout_AbortHandlerPropagates(t *testing.T) {
	log, _ := newLogger()
	h := middleware.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), middleware.Recover(log), middleware.Timeout(guard, log))
	p := servePanics(h, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if p != http.ErrAbortHandler {
		t.Errorf("panic = %v, want http.ErrAbortHandler", p)
	}
}

// chanWriter отдаёт каждую строку лога в канал: так тест дожидается записи,
// сделанной в goroutine хендлера, без sleep.
type chanWriter chan []byte

func (c chanWriter) Write(p []byte) (int, error) {
	c <- bytes.Clone(p)
	return len(p), nil
}

func TestTimeout_PanicAfterTimeoutIsLogged(t *testing.T) {
	lines := make(chanWriter, 16)
	log := logger.New(lines, slog.LevelDebug, logger.FormatJSON)
	proceed := make(chan struct{})
	h := middleware.Timeout(short, log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		<-proceed
		panic("late boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	close(proceed)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	for {
		line := receive(t, (<-chan []byte)(lines), "panic-after-timeout log")
		if bytes.Contains(line, []byte("panic after handler timeout")) {
			if !bytes.Contains(line, []byte("late boom")) {
				t.Errorf("log line misses panic value: %s", line)
			}
			return
		}
	}
}
