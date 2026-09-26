package httpserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpserver"
)

// guard — верхняя граница ожидания события. Не синхронизация: в успешном
// прогоне тест не ждёт ни миллисекунды лишней.
const guard = 5 * time.Second

// notifyListener сообщает, когда listener закрыли. Shutdown первым делом
// закрывает listener'ы, поэтому closed — надёжный сигнал «Shutdown начался».
type notifyListener struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

func (l *notifyListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type testServer struct {
	URL string
	// Stop отменяет ctx, переданный в Serve.
	Stop context.CancelFunc
	// ShutdownStarted закрывается, когда сервер закрыл listener.
	ShutdownStarted <-chan struct{}

	done chan struct{}
	err  error // пишется до close(done), читается после
}

// Wait возвращает результат Serve. Результат сохраняется, а не читается из
// канала, поэтому Wait можно звать сколько угодно раз — в том числе из cleanup.
func (s *testServer) Wait(t *testing.T) error {
	t.Helper()
	select {
	case <-s.done:
		return s.err
	case <-time.After(guard):
		t.Fatal("Serve did not return")
		return nil
	}
}

// serve запускает Serve на готовом listener'е и гарантирует, что Serve
// вернулся до окончания теста.
func serve(t *testing.T, ln net.Listener, h http.Handler, shutdown time.Duration) *testServer {
	t.Helper()
	nl := &notifyListener{Listener: ln, closed: make(chan struct{})}
	srv := httpserver.New(httpserver.Options{ShutdownTimeout: shutdown}, h, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	ts := &testServer{
		URL:             "http://" + ln.Addr().String(),
		Stop:            cancel,
		ShutdownStarted: nl.closed,
		done:            make(chan struct{}),
	}
	go func() {
		ts.err = srv.Serve(ctx, nl)
		close(ts.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(guard):
			t.Error("Serve did not return after cancel: goroutine leaked")
		}
	})
	return ts
}

// startServer поднимает сервер на 127.0.0.1:0: порт свободен, наружу не
// открыт и слушает сразу после Listen, ждать готовности не нужно.
func startServer(t *testing.T, h http.Handler, shutdown time.Duration) *testServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, ln, h, shutdown)
}

// newClient — свой клиент с таймаутом и своим транспортом, чтобы пул
// соединений не протекал между тестами.
func newClient(t *testing.T) *http.Client {
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Timeout: guard, Transport: tr}
}

type result struct {
	status int
	err    error
}

// getAsync шлёт запрос в отдельной goroutine и отдаёт результат через канал:
// t.Fatal из чужой goroutine вызывать нельзя.
func getAsync(c *http.Client, url string) <-chan result {
	ch := make(chan result, 1)
	go func() {
		resp, err := c.Get(url)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		ch <- result{status: resp.StatusCode, err: err}
	}()
	return ch
}

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

// blockingHandler сообщает о начале запроса и ждёт разрешения закончить.
// release идемпотентен и вызывается в cleanup, чтобы goroutine хендлера
// не пережила тест.
func blockingHandler(t *testing.T) (h http.Handler, started <-chan struct{}, release func()) {
	s := make(chan struct{}, 1)
	r := make(chan struct{})
	release = sync.OnceFunc(func() { close(r) })
	h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s <- struct{}{}
		<-r
		w.WriteHeader(http.StatusOK)
	})
	return h, s, release
}

func TestServe_CancelWithoutRequestsReturnsNil(t *testing.T) {
	ts := startServer(t, http.NotFoundHandler(), guard)
	ts.Stop()
	if err := ts.Wait(t); err != nil {
		t.Errorf("Serve() = %v, want nil", err)
	}
}

func TestServe_InFlightRequestSurvivesShutdown(t *testing.T) {
	h, started, release := blockingHandler(t)
	ts := startServer(t, h, guard)
	t.Cleanup(release) // LIFO: отпустит хендлер раньше, чем cleanup сервера ждёт Serve

	resp := getAsync(newClient(t), ts.URL)
	receive(t, started, "request to reach handler")

	ts.Stop()
	receive(t, ts.ShutdownStarted, "shutdown to start")
	release()

	if r := receive(t, resp, "client response"); r.err != nil || r.status != http.StatusOK {
		t.Errorf("client got status=%d err=%v, want 200 and no error", r.status, r.err)
	}
	if err := ts.Wait(t); err != nil {
		t.Errorf("Serve() = %v, want nil", err)
	}
}

func TestServe_ForcedShutdownAfterTimeout(t *testing.T) {
	h, started, release := blockingHandler(t)
	ts := startServer(t, h, 100*time.Millisecond)
	t.Cleanup(release)

	resp := getAsync(newClient(t), ts.URL)
	receive(t, started, "request to reach handler")

	ts.Stop()

	err := ts.Wait(t)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Serve() = %v, want error wrapping context.DeadlineExceeded", err)
	}
	if r := receive(t, resp, "client response"); r.err == nil {
		t.Errorf("client got status=%d, want connection error after forced close", r.status)
	}
}

func TestServe_ClosedListenerFailsImmediately(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	// ctx не отменяется: Serve обязан вернуться сам.
	ts := serve(t, ln, http.NotFoundHandler(), guard)
	err = ts.Wait(t)
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("Serve() = %v, want error wrapping net.ErrClosed", err)
	}
}

func TestRun_PortInUse(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { busy.Close() })

	srv := httpserver.New(httpserver.Options{Addr: busy.Addr().String(), ShutdownTimeout: guard},
		http.NotFoundHandler(), slog.New(slog.DiscardHandler))

	// Run в goroutine: если бы он вдруг занял порт, тест не должен зависнуть.
	ctx, cancel := context.WithCancel(t.Context())
	ch := make(chan error, 1)
	go func() { ch <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-ch
	})

	err = receive(t, ch, "Run to fail")
	ch <- err // вернуть результат для cleanup

	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "listen" {
		t.Errorf("Run() = %v, want listen error", err)
	}
}

func TestServe_RejectsOversizedHeaders(t *testing.T) {
	ts := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), guard)
	c := newClient(t)

	// Точную границу не проверяем: http.Server добавляет к лимиту 4 КиБ
	// запаса на строку запроса. Проверяем порядок величины: типичные
	// заголовки проходят, 64 КиБ — нет (при 1 МиБ или 4 ГиБ прошли бы).
	tests := []struct {
		name string
		size int
		want int
	}{
		{name: "16 KiB fits", size: 16 << 10, want: http.StatusOK},
		{name: "64 KiB rejected", size: 64 << 10, want: http.StatusRequestHeaderFieldsTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Filler", strings.Repeat("a", tt.size))
			resp, err := c.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}
