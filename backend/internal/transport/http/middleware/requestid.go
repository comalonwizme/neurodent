package middleware

import (
	"crypto/rand"
	"log/slog"
	"net/http"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
)

// Границы длины входящего X-Request-ID. Снизу: "1" или "abc" бесполезны для
// корреляции и легко совпадают. Сверху: UUID — 36 символов, ULID и наш
// rand.Text — 26; 64 оставляет запас на форматы балансировщиков и не даёт
// клиенту раздувать каждую строку лога.
const (
	minRequestIDLen = 8
	maxRequestIDLen = 64
)

// RequestID назначает запросу идентификатор: кладёт его в контекст (для
// problem-ответов), в атрибуты логов и в заголовок ответа.
//
// Входящий X-Request-ID принимается, только если он валиден по длине и
// алфавиту: так id от балансировщика связывает его логи с нашими, а
// произвольная строка от клиента не попадает в логи и заголовки ответа.
// Невалидный id молча заменяется своим.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(httpx.HeaderRequestID)
		if !validRequestID(id) {
			// 128+ бит из crypto/rand, base32 без паддинга: 26 символов [A-Z2-7].
			id = rand.Text()
		}
		w.Header().Set(httpx.HeaderRequestID, id)

		ctx := httpx.WithRequestID(r.Context(), id)
		ctx = logger.WithAttrs(ctx, slog.String("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(s string) bool {
	if len(s) < minRequestIDLen || len(s) > maxRequestIDLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
