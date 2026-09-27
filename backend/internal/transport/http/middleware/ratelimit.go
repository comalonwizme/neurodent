package middleware

import (
	"log/slog"
	"net/http"
	"slices"

	"github.com/comalonwizme/neurodent/backend/internal/platform/clientip"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit"
)

// RateLimitOptions — настройки грубого лимита (ADR-0016).
type RateLimitOptions struct {
	Limiter ratelimit.Limiter
	// Secret — HMAC-секрет ключей (config.RateLimitKey).
	Secret []byte
	// Anon — лимит анонимных запросов по IP клиента (IPv6 — по /64).
	Anon ratelimit.Policy
	// User — лимит аутентифицированного пользователя.
	User ratelimit.Policy
	// Resolver определяет IP клиента с учётом доверенных прокси.
	Resolver *clientip.Resolver
	// Principal возвращает пользователя запроса. Его даёт аутентификация
	// IAM, стоящая в цепочке перед RateLimit; до IAM — nil, и все запросы
	// анонимные.
	Principal func(r *http.Request) (userID string, ok bool)
	// Exempt — пути без лимита (точное совпадение): пробы оркестратора не
	// должны получать 429 и снимать под с балансировки.
	Exempt []string
}

// RateLimit ограничивает частоту запросов: аутентифицированных — по
// пользователю, анонимных — по IP. По IP лимитировать аутентифицированных
// нельзя: за CGNAT мобильных операторов и NAT клиники один адрес делят
// сотни людей.
//
// Кладёт IP клиента в контекст (clientip.WithIP) — в лог он не пишется.
// Превышение — 429 RFC 9457 с Retry-After. Сбой лимитера — запрос
// пропускается, ошибка в лог (fail open): грубая защита не должна
// превращать сбой хранилища в отказ всего API.
func RateLimit(o RateLimitOptions, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(o.Exempt, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			ip, ipOK := o.Resolver.ClientIP(r)
			if ipOK {
				r = r.WithContext(clientip.WithIP(r.Context(), ip))
			}

			var pol ratelimit.Policy
			var key ratelimit.Key
			if user, ok := principal(o, r); ok {
				pol, key = o.User, ratelimit.NewKey(o.Secret, o.User.Name, user)
			} else if ipOK {
				pol, key = o.Anon, ratelimit.NewKey(o.Secret, o.Anon.Name, clientip.LimitKey(ip))
			} else {
				next.ServeHTTP(w, r) // не TCP (unix-сокет): считать не по чему
				return
			}

			d, err := o.Limiter.Allow(r.Context(), pol, key)
			if err != nil {
				log.ErrorContext(r.Context(), "rate limiter failed, request allowed", "policy", pol.Name, "err", err)
				next.ServeHTTP(w, r)
				return
			}
			if !d.Allowed {
				httpx.WriteError(w, r, log, ratelimit.Limited(d.RetryAfter))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func principal(o RateLimitOptions, r *http.Request) (string, bool) {
	if o.Principal == nil {
		return "", false
	}
	return o.Principal(r)
}
