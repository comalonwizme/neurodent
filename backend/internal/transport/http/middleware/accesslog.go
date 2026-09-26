package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// RouteMatcher находит шаблон маршрута для запроса. *http.ServeMux
// реализует его методом Handler.
type RouteMatcher interface {
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// routeUnmatched пишется вместо шаблона, если маршрут не найден.
const routeUnmatched = "unmatched"

// AccessLog пишет одну строку на запрос: метод, шаблон маршрута, статус,
// длительность, размер ответа; request_id приходит из контекста.
//
// В лог попадает шаблон ("GET /patients/{id}"), а не путь: путь может
// содержать идентификаторы и PHI. По той же причине не пишутся query,
// заголовки, IP и User-Agent — всё это персональные данные.
//
// Шаблон берётся у роутера до вызова хендлера, а не из r.Pattern после:
// middleware ниже передают дальше копию запроса (r.WithContext), и
// r.Pattern, выставленный роутером, на наш экземпляр не попадёт.
func AccessLog(log *slog.Logger, routes RouteMatcher) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			route := routeOf(routes, r)
			rw := &responseWriter{ResponseWriter: w}

			// defer: строка пишется и для запроса, оборванного через
			// http.ErrAbortHandler.
			defer func() {
				log.LogAttrs(r.Context(), slog.LevelInfo, "http request",
					slog.String("method", r.Method),
					slog.String("route", route),
					slog.Int("status", rw.finalStatus()),
					slog.Duration("duration", time.Since(start)),
					slog.Int64("bytes", rw.bytes),
				)
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

func routeOf(routes RouteMatcher, r *http.Request) string {
	// Для CONNECT ServeMux возвращает вместо шаблона сырой путь редиректа.
	// Маршрутов CONNECT у API нет, путь в лог не пускаем.
	if r.Method == http.MethodConnect {
		return routeUnmatched
	}
	if _, pattern := routes.Handler(r); pattern != "" {
		return pattern
	}
	return routeUnmatched
}
