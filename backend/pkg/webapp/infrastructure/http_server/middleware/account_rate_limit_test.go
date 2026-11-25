package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func drive(t *testing.T, l *AccountRateLimiter, accountID string, n int) (allowed, denied int) {
	t.Helper()
	e := echo.New()
	h := l.Middleware()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if accountID != "" {
			c.Set("accountID", accountID)
		}
		err := h(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok && he.Code == http.StatusTooManyRequests {
				denied++
				continue
			}
			t.Fatalf("unexpected error: %v", err)
		}
		allowed++
	}
	return allowed, denied
}

func newTestLimiter(rps float64, burst int) *AccountRateLimiter {
	l := NewAccountRateLimiter(rps, burst, slog.New(slog.DiscardHandler))
	// Freeze the clock so the bucket cannot refill mid-test and make the
	// assertions timing-dependent.
	frozen := time.Unix(1_800_000_000, 0)
	l.nowOverride = func() time.Time { return frozen }
	return l
}

func TestBurstIsAllowedThenTheAccountIsThrottled(t *testing.T) {
	l := newTestLimiter(1, 5)
	allowed, denied := drive(t, l, uuid.NewString(), 12)
	if allowed != 5 {
		t.Errorf("allowed = %d, want the full burst of 5", allowed)
	}
	if denied != 7 {
		t.Errorf("denied = %d, want the remaining 7", denied)
	}
}

// The whole point of keying on the account: one tenant exhausting its budget
// must not affect another's.
func TestOneAccountsBudgetDoesNotAffectAnother(t *testing.T) {
	l := newTestLimiter(1, 3)
	noisy, quiet := uuid.NewString(), uuid.NewString()

	if _, denied := drive(t, l, noisy, 10); denied == 0 {
		t.Fatal("the noisy account was never throttled; the test proves nothing")
	}
	allowed, denied := drive(t, l, quiet, 3)
	if allowed != 3 || denied != 0 {
		t.Errorf("the quiet account got %d allowed / %d denied, want 3/0", allowed, denied)
	}
}

// No account on the context means the chain was wired wrong or the route is not
// really authenticated. Either way a limiter must never be the thing that
// refuses a request the real auth would have accepted.
func TestRequestsWithNoAccountPassThrough(t *testing.T) {
	l := newTestLimiter(1, 1)
	allowed, denied := drive(t, l, "", 20)
	if denied != 0 {
		t.Errorf("denied = %d, want 0 — an unauthenticated request must not be rate limited here", denied)
	}
	if allowed != 20 {
		t.Errorf("allowed = %d, want 20", allowed)
	}
}

// 0 = unlimited, the repo's quota convention, so an operator can switch this
// off without a rebuild.
func TestZeroDisablesTheLimiter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rps        float64
		burst      int
		wantEnable bool
	}{
		{"both zero", 0, 0, false},
		{"zero rate", 0, 100, false},
		{"zero burst", 20, 0, false},
		{"configured", 20, 100, true},
	} {
		l := NewAccountRateLimiter(tc.rps, tc.burst, slog.New(slog.DiscardHandler))
		if l.Enabled() != tc.wantEnable {
			t.Errorf("%s: Enabled() = %v, want %v", tc.name, l.Enabled(), tc.wantEnable)
		}
	}
	l := newTestLimiter(0, 0)
	if _, denied := drive(t, l, uuid.NewString(), 50); denied != 0 {
		t.Errorf("a disabled limiter denied %d requests", denied)
	}
}

// A refusal has to tell a script when to come back, or it retries immediately
// and the limiter converts one busy caller into a tight loop.
func TestARefusalCarriesRetryAfter(t *testing.T) {
	l := newTestLimiter(1, 1)
	e := echo.New()
	h := l.Middleware()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	accountID := uuid.NewString()

	var rec *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		rec = httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set("accountID", accountID)
		_ = h(c)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("a throttled response carries no Retry-After")
	}
}

// The map is keyed by account and an instance serves an unbounded number of
// them over its life. Without the sweep this is a slow leak that only shows on
// a long-lived process — the one class of bug a rate limiter must not add.
func TestIdleBucketsAreReaped(t *testing.T) {
	l := NewAccountRateLimiter(1, 5, slog.New(slog.DiscardHandler))
	now := time.Unix(1_800_000_000, 0)
	l.nowOverride = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		l.allow(uuid.NewString())
	}
	if got := len(l.buckets); got != 50 {
		t.Fatalf("buckets = %d, want 50 before any sweep", got)
	}

	// Move past the idle TTL and touch one account: the sweep runs from the
	// request path, so it needs a request to happen.
	now = now.Add(bucketIdleTTL + bucketSweepInterval + time.Second)
	survivor := uuid.NewString()
	l.allow(survivor)

	if got := len(l.buckets); got != 1 {
		t.Errorf("buckets = %d after the sweep, want only the live one", got)
	}
	if _, ok := l.buckets[survivor]; !ok {
		t.Error("the sweep dropped the account that just made a request")
	}
}

// A bucket that is still being used must survive the sweep, or a steady caller
// silently gets a fresh burst allowance every ten minutes.
func TestActiveBucketsSurviveTheSweep(t *testing.T) {
	l := NewAccountRateLimiter(1, 5, slog.New(slog.DiscardHandler))
	now := time.Unix(1_800_000_000, 0)
	l.nowOverride = func() time.Time { return now }

	active := uuid.NewString()
	for i := 0; i < 15; i++ {
		l.allow(active)
		now = now.Add(bucketSweepInterval + time.Second)
	}
	if _, ok := l.buckets[active]; !ok {
		t.Fatal("a continuously active account lost its bucket")
	}
}
