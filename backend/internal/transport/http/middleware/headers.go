package middleware

import (
	"fmt"
	"net/http"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
)

// hstsValue — два года: столько требует список preload браузеров, и это
// стандартная рекомендация. preload сознательно не включаем: выйти из
// списка можно только за месяцы, решение принимается отдельно.
const hstsValue = "max-age=63072000; includeSubDomains"

// SecurityHeaders ставит заголовки безопасности для JSON API.
//
//   - nosniff: браузер не угадывает тип ответа (JSON не исполнится как скрипт);
//   - CSP default-src 'none': ответ API ничего не загружает и не встраивается;
//     frame-ancestors 'none' запрещает показывать его во фрейме (clickjacking);
//   - Referrer-Policy no-referrer: URL API (с id в пути) не утекает в Referer;
//   - Cache-Control no-store по умолчанию: ответы с PHI не оседают в кешах
//     прокси и браузера; хендлер может переопределить для публичных данных;
//   - HSTS только вне dev: в dev нет TLS, а HSTS на localhost ломает другие
//     локальные проекты на годы.
func SecurityHeaders(hsts bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			if hsts {
				h.Set("Strict-Transport-Security", hstsValue)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit ограничивает тело запроса n байтами: сверх лимита — 413.
//
// Content-Length проверяется сразу, до хендлера: честный клиент с большим
// телом получает 413 без чтения. Для chunked-тела (длина неизвестна) работает
// http.MaxBytesReader: чтение сверх лимита вернёт *http.MaxBytesError, и
// httpx.WriteError превратит её в 413.
func BodyLimit(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, httpx.CodePayloadTooLarge,
					fmt.Sprintf("request body must not exceed %d bytes", n))
				return
			}
			// Копия запроса: net/http запрещает хендлерам менять исходный *Request.
			r = r.WithContext(r.Context())
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
