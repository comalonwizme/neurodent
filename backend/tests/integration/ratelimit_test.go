//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit/ratelimittest"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

func testRateLimit(t *testing.T, env *pgEnv) {
	// Тот же набор кейсов, что у реализации в памяти: одна таблица на интерфейс.
	t.Run("shared limiter suite", func(t *testing.T) {
		ratelimittest.Run(t, func(c clock.Clock) ratelimit.Limiter { return ratelimit.NewPostgres(env.db, c) })
	})

	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	pol := ratelimit.Policy{Name: "iam.login.phone", Limit: 5, Window: 15 * time.Minute}

	t.Run("counter is written outside the business transaction", func(t *testing.T) {
		l := ratelimit.NewPostgres(env.db, clock.NewManual(start))
		var inner error
		err := env.db.WithinTx(t.Context(), scope.Global(scope.Anonymous()), func(ctx context.Context) error {
			_, inner = l.Allow(ctx, pol, ratelimit.NewKey([]byte("s"), pol.Name, "+77010000000"))
			return nil
		})
		if err != nil || !errors.Is(inner, postgres.ErrInsideTx) {
			t.Errorf("Allow inside a transaction = %v (outer %v), want ErrInsideTx", inner, err)
		}
	})

	t.Run("table holds HMAC keys, not the values", func(t *testing.T) {
		const phone = "+77019998877"
		l := ratelimit.NewPostgres(env.db, clock.NewManual(start))
		if _, err := l.Allow(t.Context(), pol, ratelimit.NewKey([]byte("secret-for-test-0123456789abcdef"), pol.Name, phone)); err != nil {
			t.Fatal(err)
		}
		var leaked bool
		err := ownerConn(t, env).QueryRow(t.Context(),
			"SELECT EXISTS (SELECT 1 FROM platform.rate_limit_counters WHERE position(convert_to($1, 'UTF8') IN key) > 0)", "77019998877").Scan(&leaked)
		if err != nil || leaked {
			t.Errorf("phone found in counters: %v %v", leaked, err)
		}
	})

	t.Run("expired windows are deleted in batches", func(t *testing.T) {
		c := clock.NewManual(start)
		l := ratelimit.NewPostgres(env.db, c)
		for _, v := range []string{"a", "b", "c"} {
			if _, err := l.Allow(t.Context(), pol, ratelimit.NewKey([]byte("gc"), pol.Name, v, start.String())); err != nil {
				t.Fatal(err)
			}
		}
		c.Advance(time.Hour) // все три окна истекли
		first, err := l.DeleteExpired(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if first != 2 {
			t.Errorf("first batch deleted %d, want the batch size 2", first)
		}
		var total int64
		for {
			n, err := l.DeleteExpired(t.Context(), 1000)
			if err != nil {
				t.Fatal(err)
			}
			total += n
			if n == 0 {
				break
			}
		}
		if total < 1 {
			t.Errorf("remaining expired windows not deleted")
		}
	})
}
