// Package ratelimit — лимиты частоты запросов (ADR-0016).
//
// Один интерфейс, две реализации:
//   - Memory — счётчик в памяти реплики для грубых лимитов middleware
//     (анонимные запросы по IP, аутентифицированные — по пользователю). В
//     БД ничего не пишет: иначе лимитер стал бы усилителем нагрузки на БД;
//   - Postgres — общий для всех реплик счётчик для точных лимитов IAM
//     (вход, OTP по аккаунту и номеру).
//
// Алгоритм — fixed window: «не больше Limit за окно Window». На стыке окон
// возможно до 2 × Limit; для лимитов входа важнее простота и
// объяснимость, стык перекрывает блокировка аккаунта в IAM.
package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// Policy — лимит: не больше Limit запросов за окно Window.
type Policy struct {
	Name   string // часть ключа и метка в логах: "anon.ip", "user", "iam.login.phone"
	Limit  int
	Window time.Duration
}

// Decision — результат проверки.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration // > 0, если Allowed == false: до конца окна
}

// Limiter считает запросы по ключу.
type Limiter interface {
	Allow(ctx context.Context, p Policy, key Key) (Decision, error)
}

// Key — HMAC-SHA256 от политики и частей ключа. Получить его можно только
// через NewKey: в хранилище счётчиков не попадает ни IP, ни телефон.
type Key [sha256.Size]byte

// NewKey вычисляет ключ. Простой SHA-256 не годится: пространство номеров
// телефонов (~10¹⁰) и IPv4 (2³²) перебирается за секунды, так что хеш без
// секрета обратим.
func NewKey(secret []byte, policy string, parts ...string) Key {
	mac := hmac.New(sha256.New, secret)
	write := func(s string) {
		// Длина перед значением: ("ab","c") и ("a","bc") дают разные ключи.
		_, _ = fmt.Fprintf(mac, "%d:%s", len(s), s)
	}
	write(policy)
	for _, p := range parts {
		write(p)
	}
	var k Key
	copy(k[:], mac.Sum(nil))
	return k
}

// window возвращает начало и конец окна, в которое попадает now.
func window(now time.Time, d time.Duration) (start, end time.Time) {
	start = now.Truncate(d)
	return start, start.Add(d)
}

func decide(hits int, p Policy, now, end time.Time) Decision {
	if hits <= p.Limit {
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: end.Sub(now)}
}

// Limited — ошибка превышения лимита для httpx.WriteError: 429 RFC 9457 с
// заголовком Retry-After.
func Limited(retryAfter time.Duration) error {
	return apperr.Wrap(apperr.RateLimited, "too many requests, retry later", &limitedError{retryAfter: retryAfter})
}

type limitedError struct{ retryAfter time.Duration }

func (e *limitedError) Error() string {
	return fmt.Sprintf("rate limit exceeded, retry after %s", e.retryAfter)
}

// RetryAfter — через сколько повторить; его читает httpx.WriteError.
func (e *limitedError) RetryAfter() time.Duration { return e.retryAfter }
