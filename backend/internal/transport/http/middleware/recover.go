package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// panicError переносит панику из goroutine хендлера (Timeout) в goroutine
// запроса вместе с исходным стеком: стек места re-panic бесполезен.
type panicError struct {
	value any
	stack []byte
}

// Recover превращает панику хендлера в 500 (RFC 9457) и пишет стек в лог.
//
// Без него net/http сам перехватит панику, но клиент получит оборванное
// соединение, а в лог попадёт стек без request_id.
//
// Два случая, когда Recover паникует сам — это не сбой, а штатный механизм
// net/http (http.ErrAbortHandler обрывает соединение без записи в лог):
//   - хендлер паникует с http.ErrAbortHandler намеренно — пропускаем дальше;
//   - ответ уже начат: дописать 500 нельзя, а оставить обрезанный ответ со
//     статусом 200 — значит выдать его клиенту за успешный. Обрываем.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &responseWriter{ResponseWriter: w}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				value := p
				var stack []byte
				if pe, ok := p.(*panicError); ok {
					value, stack = pe.value, pe.stack
				} else {
					stack = debug.Stack()
				}
				if err, ok := value.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(http.ErrAbortHandler)
				}

				log.ErrorContext(r.Context(), "panic recovered",
					"panic", fmt.Sprint(value),
					"stack", string(stack),
				)
				if rw.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				httpx.WriteProblem(rw, r, http.StatusInternalServerError, apperr.Internal.String(), "")
			}()
			next.ServeHTTP(rw, r)
		})
	}
}
