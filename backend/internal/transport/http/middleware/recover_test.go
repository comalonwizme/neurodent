package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

func panickingHandler(http.ResponseWriter, *http.Request) {
	panic("boom")
}

func TestRecover_PanicBecomes500WithStackInLog(t *testing.T) {
	log, logs := newLogger()
	h := middleware.Chain(http.HandlerFunc(panickingHandler), middleware.RequestID, middleware.Recover(log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	p := decodeProblem(t, rec)
	if p.Code != "internal" || p.Detail != "internal error" || p.RequestID == "" {
		t.Errorf("problem = %+v", p)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("panic value leaked to client: %s", rec.Body.String())
	}

	r := findRecord(t, logs.records(t), "panic recovered")
	if r["panic"] != "boom" {
		t.Errorf("logged panic = %v, want boom", r["panic"])
	}
	if stack, _ := r["stack"].(string); !strings.Contains(stack, "panickingHandler") {
		t.Errorf("stack does not point at the panicking handler:\n%s", stack)
	}
	if r["request_id"] != p.RequestID {
		t.Errorf("log request_id = %v, response request_id = %s", r["request_id"], p.RequestID)
	}
}

func TestRecover_NoPanicPassesThrough(t *testing.T) {
	log, logs := newLogger()
	h := middleware.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "ok")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "ok" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if logs.String() != "" {
		t.Errorf("unexpected logs: %s", logs.String())
	}
}

func TestRecover_AbortHandlerPropagatesSilently(t *testing.T) {
	log, logs := newLogger()
	h := middleware.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	p := servePanics(h, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !isAbort(p) {
		t.Errorf("panic = %v, want http.ErrAbortHandler", p)
	}
	if logs.String() != "" {
		t.Errorf("intentional abort must not be logged as a crash: %s", logs.String())
	}
}

func TestRecover_PanicAfterResponseStartedAbortsConnection(t *testing.T) {
	log, logs := newLogger()
	h := middleware.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"partial":`)
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	p := servePanics(h, rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// Дописать 500 после начатого 200 нельзя; обрыв соединения — единственный
	// способ не выдать обрезанный ответ за успешный.
	if !isAbort(p) {
		t.Errorf("panic = %v, want http.ErrAbortHandler", p)
	}
	findRecord(t, logs.records(t), "panic recovered")
}

// TestRecover_AbortOverRealConnection проверяет то же на реальном сервере:
// клиент получает ошибку соединения, а не 200 с обрезанным телом.
func TestRecover_AbortOverRealConnection(t *testing.T) {
	log, _ := newLogger()
	srv := httptest.NewServer(middleware.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{"partial":`)
		panic("boom")
	})))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	if err == nil {
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
	}
	if err == nil {
		t.Error("client read a truncated response without error")
	}
}
