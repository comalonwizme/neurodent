package ratelimit_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit/ratelimittest"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

func TestMemory(t *testing.T) {
	ratelimittest.Run(t, func(c clock.Clock) ratelimit.Limiter { return ratelimit.NewMemory(c, 1000) })
}

func TestMemory_KeyCountIsBounded(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	m := ratelimit.NewMemory(c, 3)
	p := ratelimit.Policy{Name: "ip", Limit: 1, Window: time.Minute}
	secret := []byte("secret")
	for i := range 10 {
		if d, _ := m.Allow(t.Context(), p, ratelimit.NewKey(secret, "ip", string(rune('a'+i)))); !d.Allowed {
			t.Errorf("new key %d denied: overflow must fail open", i)
		}
	}
	if m.Len() != 3 {
		t.Errorf("tracked keys = %d, want the bound 3", m.Len())
	}
	// Окна истекли — место освобождается, новые ключи снова считаются.
	c.Advance(time.Minute)
	k := ratelimit.NewKey(secret, "ip", "fresh")
	if _, err := m.Allow(t.Context(), p, k); err != nil {
		t.Fatal(err)
	}
	if d, _ := m.Allow(t.Context(), p, k); d.Allowed {
		t.Error("key after sweep is not tracked")
	}
}

func TestNewKey(t *testing.T) {
	s := []byte("secret-1")
	a := ratelimit.NewKey(s, "iam.login.phone", "+77010000000")
	if a != ratelimit.NewKey(s, "iam.login.phone", "+77010000000") {
		t.Error("key is not deterministic")
	}
	distinct := []ratelimit.Key{
		ratelimit.NewKey([]byte("secret-2"), "iam.login.phone", "+77010000000"),
		ratelimit.NewKey(s, "iam.otp.phone", "+77010000000"),
		ratelimit.NewKey(s, "iam.login.phone", "+77010000001"),
		ratelimit.NewKey(s, "ab", "c"),
		ratelimit.NewKey(s, "a", "bc"),
	}
	for i, k := range distinct {
		if k == a {
			t.Errorf("key %d collides", i)
		}
	}
	if distinct[3] == distinct[4] {
		t.Error("parts are not length-prefixed: (ab,c) == (a,bc)")
	}
	if bytes.Contains(a[:], []byte("7701")) {
		t.Error("key contains the plain value")
	}
}

func TestLimited(t *testing.T) {
	err := ratelimit.Limited(30 * time.Second)
	if apperr.KindOf(err) != apperr.RateLimited {
		t.Errorf("kind = %v", apperr.KindOf(err))
	}
	ra, ok := errors.AsType[interface {
		error
		RetryAfter() time.Duration
	}](err)
	if !ok || ra.RetryAfter() != 30*time.Second {
		t.Errorf("RetryAfter not reachable: %v", err)
	}
}
