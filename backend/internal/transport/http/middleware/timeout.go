package middleware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// Timeout ограничивает время хендлера: по истечении d клиент получает 503
// в формате RFC 9457, а контекст хендлера отменяется.
//
// Почему не http.TimeoutHandler: он отдаёт фиксированное тело text/plain
// без request_id и без Content-Type, а единый формат ошибок — контракт API.
// Механика та же: хендлер пишет в буфер в своей goroutine, и ответ уходит
// клиенту целиком — либо ответ хендлера, либо 503, но не их смесь.
//
// Следствия буферизации: нет потоковой отдачи (Flush). Эндпоинты со
// стримингом (например, ответы AI-ассистента) нужно будет монтировать мимо
// этого middleware со своим таймаутом.
//
// d должен быть меньше http.Server.WriteTimeout (инвариант в config): иначе
// соединение оборвётся раньше, чем мы успеем записать 503.
func Timeout(d time.Duration, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			tw := &timeoutWriter{header: w.Header().Clone()}
			done := make(chan struct{})
			panicked := make(chan *panicError, 1)

			go func() {
				defer func() {
					if p := recover(); p != nil {
						tw.forwardPanic(ctx, log, &panicError{value: p, stack: debug.Stack()}, panicked)
						return
					}
					close(done)
				}()
				next.ServeHTTP(tw, r.WithContext(ctx))
			}()

			select {
			case <-done:
				tw.flushTo(w)
			case pe := <-panicked:
				// Паника передаётся в goroutine запроса, где её поймает Recover.
				panic(pe)
			case <-ctx.Done():
				tw.mu.Lock()
				tw.timedOut = true
				tw.mu.Unlock()
				// Паника могла случиться до того, как мы взяли lock: forwardPanic
				// кладёт её в канал под тем же lock, так что здесь она уже видна.
				select {
				case pe := <-panicked:
					panic(pe)
				default:
				}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					log.WarnContext(r.Context(), "handler timed out", "timeout", d)
					httpx.WriteProblem(w, r, http.StatusServiceUnavailable, apperr.Unavailable.String(), "")
				}
				// Иначе клиент ушёл сам (родительский контекст отменён):
				// писать некому.
			}
		})
	}
}

// timeoutWriter буферизует ответ хендлера. После таймаута запись
// отклоняется с http.ErrHandlerTimeout: хендлер, проверяющий ошибки записи,
// узнает, что его ответ никому не нужен.
type timeoutWriter struct {
	mu          sync.Mutex
	header      http.Header
	buf         bytes.Buffer
	status      int
	wroteHeader bool
	timedOut    bool
}

func (tw *timeoutWriter) Header() http.Header { return tw.header }

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut || tw.wroteHeader || code < http.StatusOK {
		return
	}
	tw.status = code
	tw.wroteHeader = true
}

func (tw *timeoutWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	if !tw.wroteHeader {
		tw.status = http.StatusOK
		tw.wroteHeader = true
	}
	return tw.buf.Write(p)
}

// forwardPanic отдаёт панику goroutine запроса, если та ещё ждёт. После
// таймаута ждать некому: панику только логируем, иначе она пропадёт молча.
func (tw *timeoutWriter) forwardPanic(ctx context.Context, log *slog.Logger, pe *panicError, ch chan<- *panicError) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		log.ErrorContext(ctx, "panic after handler timeout", "panic", fmt.Sprint(pe.value), "stack", string(pe.stack))
		return
	}
	ch <- pe // буфер 1, паника случается один раз: не блокирует
}

// flushTo отдаёт буферизованный ответ. Вызывается только после завершения
// хендлера, поэтому header и buf больше никто не трогает.
func (tw *timeoutWriter) flushTo(w http.ResponseWriter) {
	dst := w.Header()
	// Заменяем целиком: хендлер мог не только добавить, но и удалить заголовок.
	clear(dst)
	maps.Copy(dst, tw.header)
	status := tw.status
	if !tw.wroteHeader {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	// Ошибка записи — клиент ушёл; статус уже учтён access-логом.
	_, _ = w.Write(tw.buf.Bytes())
}
