package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/platform/clientip"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

// recordingLimiter запоминает, какой политикой и ключом его спросили.
type recordingLimiter struct {
	policy string
	key    ratelimit.Key
	deny   bool
	err    error
}

func (l *recordingLimiter) Allow(_ context.Context, p ratelimit.Policy, k ratelimit.Key) (ratelimit.Decision, error) {
	l.policy, l.key = p.Name, k
	if l.err != nil {
		return ratelimit.Decision{}, l.err
	}
	if l.deny {
		return ratelimit.Decision{RetryAfter: 1500 * time.Millisecond}, nil
	}
	return ratelimit.Decision{Allowed: true}, nil
}

var secret = []byte("test-secret-0123456789abcdef0123")

func rlOptions(l ratelimit.Limiter, principal func(*http.Request) (string, bool)) middleware.RateLimitOptions {
	return middleware.RateLimitOptions{
		Limiter:   l,
		Secret:    secret,
		Anon:      ratelimit.Policy{Name: "anon.ip", Limit: 2, Window: time.Minute},
		User:      ratelimit.Policy{Name: "user", Limit: 2, Window: time.Minute},
		Resolver:  clientip.NewResolver(nil),
		Principal: principal,
		Exempt:    []string{"/healthz", "/readyz"},
	}
}

func serveRL(t *testing.T, o middleware.RateLimitOptions, path, remote string) (*httptest.ResponseRecorder, netip.Addr) {
	t.Helper()
	log, _ := newLogger()
	var seen netip.Addr
	h := middleware.RateLimit(o, log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = clientip.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

func TestRateLimit_KeyChoice(t *testing.T) {
	anon := &recordingLimiter{}
	_, seen := serveRL(t, rlOptions(anon, nil), "/x", "203.0.113.9:5000")
	if anon.policy != "anon.ip" || anon.key != ratelimit.NewKey(secret, "anon.ip", "203.0.113.9") {
		t.Errorf("anonymous request limited by %s", anon.policy)
	}
	if seen.String() != "203.0.113.9" {
		t.Errorf("client IP in context = %v", seen)
	}

	v6 := &recordingLimiter{}
	serveRL(t, rlOptions(v6, nil), "/x", "[2001:db8:1:2:aaaa::1]:5000")
	if v6.key != ratelimit.NewKey(secret, "anon.ip", "2001:db8:1:2::/64") {
		t.Error("IPv6 client is not keyed by its /64")
	}

	user := &recordingLimiter{}
	serveRL(t, rlOptions(user, func(*http.Request) (string, bool) { return "user-42", true }), "/x", "203.0.113.9:5000")
	if user.policy != "user" || user.key != ratelimit.NewKey(secret, "user", "user-42") {
		t.Errorf("authenticated request limited by %s, want the user key (CGNAT)", user.policy)
	}
}

func TestRateLimit_DeniedIs429WithRetryAfter(t *testing.T) {
	rec, _ := serveRL(t, rlOptions(&recordingLimiter{deny: true}, nil), "/x", "203.0.113.9:5000")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("got %d Retry-After %q, want 429 and 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	if p := decodeProblem(t, rec); p.Code != "rate_limited" {
		t.Errorf("problem = %+v", p)
	}
}

func TestRateLimit_ProbesAreExempt(t *testing.T) {
	l := &recordingLimiter{deny: true}
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec, _ := serveRL(t, rlOptions(l, nil), path, "10.0.0.1:5000"); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, probes must never be limited", path, rec.Code)
		}
	}
	if l.policy != "" {
		t.Error("limiter consulted for a probe")
	}
}

func TestRateLimit_FailsOpen(t *testing.T) {
	rec, _ := serveRL(t, rlOptions(&recordingLimiter{err: errors.New("storage down")}, nil), "/x", "203.0.113.9:5000")
	if rec.Code != http.StatusOK {
		t.Errorf("limiter error → %d, want the request allowed", rec.Code)
	}
}

func TestRateLimit_RealLimiterCountsPerClient(t *testing.T) {
	o := rlOptions(ratelimit.NewMemory(clock.NewManual(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)), 100), nil)
	codes := func(remote string) []int {
		var out []int
		for range 3 {
			rec, _ := serveRL(t, o, "/x", remote)
			out = append(out, rec.Code)
		}
		return out
	}
	if got := codes("203.0.113.9:1"); got[0] != 200 || got[1] != 200 || got[2] != 429 {
		t.Errorf("client a: %v, want 200 200 429", got)
	}
	if got := codes("203.0.113.10:1"); got[0] != 200 {
		t.Errorf("client b limited by client a: %v", got)
	}
}
