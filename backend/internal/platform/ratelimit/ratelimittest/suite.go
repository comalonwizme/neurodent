// Package ratelimittest — общий набор тестов для реализаций
// ratelimit.Limiter: одна таблица кейсов на интерфейс (ADR-0016). Его
// запускают unit-тесты Memory и integration-тесты Postgres.
package ratelimittest

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

// Run проверяет Limiter, созданный newLimiter поверх ручных часов.
func Run(t *testing.T, newLimiter func(c clock.Clock) ratelimit.Limiter) {
	t.Helper()
	secret := []byte(rand.Text())
	// Случайная часть ключа: реализации на общей БД не мешают друг другу.
	key := func(parts ...string) ratelimit.Key {
		return ratelimit.NewKey(secret, "test", append(parts, rand.Text())...)
	}
	start := time.Date(2026, 9, 27, 12, 0, 10, 0, time.UTC)
	pol := ratelimit.Policy{Name: "test", Limit: 3, Window: time.Minute}

	t.Run("allows up to the limit, then denies until the window ends", func(t *testing.T) {
		c := clock.NewManual(start)
		l := newLimiter(c)
		k := key()
		for i := range 3 {
			if d := allow(t, l, pol, k); !d.Allowed {
				t.Fatalf("request %d denied", i+1)
			}
		}
		d := allow(t, l, pol, k)
		if d.Allowed || d.RetryAfter != 50*time.Second {
			t.Errorf("4th request = %+v, want denied with RetryAfter 50s", d)
		}
		c.Advance(49 * time.Second)
		if d := allow(t, l, pol, k); d.Allowed || d.RetryAfter != time.Second {
			t.Errorf("at 12:00:59 = %+v, want denied with RetryAfter 1s", d)
		}
		c.Advance(time.Second) // 12:01:00 — новое окно
		if d := allow(t, l, pol, k); !d.Allowed {
			t.Errorf("new window = %+v, want allowed", d)
		}
	})

	t.Run("keys and policies are independent", func(t *testing.T) {
		l := newLimiter(clock.NewManual(start))
		a, b := key("a"), key("b")
		for range 3 {
			allow(t, l, pol, a)
		}
		if d := allow(t, l, pol, b); !d.Allowed {
			t.Error("another key is limited by the first one")
		}
		other := ratelimit.Policy{Name: "other", Limit: 1, Window: time.Minute}
		if d := allow(t, l, other, key("c")); !d.Allowed {
			t.Error("another policy is limited")
		}
	})

	t.Run("concurrent requests never exceed the limit", func(t *testing.T) {
		l := newLimiter(clock.NewManual(start))
		k := key()
		const workers = 40
		big := ratelimit.Policy{Name: "test", Limit: 10, Window: time.Minute}
		var wg sync.WaitGroup
		allowed := make([]bool, workers)
		errs := make([]error, workers)
		for i := range workers {
			wg.Go(func() {
				d, err := l.Allow(context.Background(), big, k)
				allowed[i], errs[i] = d.Allowed, err
			})
		}
		wg.Wait()
		n := 0
		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("worker %d: %v", i, errs[i])
			}
			if allowed[i] {
				n++
			}
		}
		if n != big.Limit {
			t.Errorf("%d of %d concurrent requests allowed, want exactly %d", n, workers, big.Limit)
		}
	})
}

func allow(t *testing.T, l ratelimit.Limiter, p ratelimit.Policy, k ratelimit.Key) ratelimit.Decision {
	t.Helper()
	d, err := l.Allow(t.Context(), p, k)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
