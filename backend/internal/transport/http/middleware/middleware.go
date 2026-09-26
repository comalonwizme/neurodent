// Package middleware — сквозная обработка HTTP-запросов: request-id,
// access-лог, security-заголовки, recover, лимит тела, таймаут.
//
// Каждый middleware — func(http.Handler) http.Handler и ничего не знает
// о соседях. Порядок цепочки задаётся в одном месте (app.newRouter) и там же
// обоснован: от него зависят формат ответа при панике и таймауте.
package middleware

import "net/http"

// Middleware — обёртка над обработчиком.
type Middleware = func(http.Handler) http.Handler

// Chain оборачивает h так, что mws[0] — самый внешний: запрос проходит
// middleware в порядке перечисления, ответ — в обратном.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// responseWriter запоминает итоговый статус и размер ответа: они нужны
// access-логу, а recover — знать, начат ли уже ответ.
type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(code int) {
	// 1xx — промежуточные ответы (103 Early Hints), за ними следует
	// финальный статус; запоминаем только финальный.
	if !w.wroteHeader && code >= http.StatusOK {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Unwrap открывает исходный writer для http.ResponseController.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finalStatus — статус, который увидел клиент. Хендлер, не записавший
// ничего, получает 200 от net/http.
func (w *responseWriter) finalStatus() int {
	if !w.wroteHeader {
		return http.StatusOK
	}
	return w.status
}
