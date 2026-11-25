package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

// bucketIdleTTL is how long an account's bucket survives with no traffic before
// the sweeper drops it.
//
// It exists because the map is keyed by account and an instance serves an
// unbounded number of them over its lifetime; without a sweep this is a slow
// leak that only shows up on a long-lived process, which is exactly the kind of
// bug a rate limiter must not introduce. Ten minutes is far longer than any
// burst it needs to remember and far shorter than an uptime.
const bucketIdleTTL = 10 * time.Minute

// bucketSweepInterval is how often idle buckets are reaped. Deliberately lazy —
// the sweep runs from the request path rather than a goroutine, so a limiter
// that is never used costs nothing and nothing has to be shut down.
const bucketSweepInterval = time.Minute

type accountBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// AccountRateLimiter bounds the authenticated API per ACCOUNT.
//
// Per account, not per user and not per IP, because the account is the tenancy
// unit: one tenant running a script from ten machines is still one tenant, and
// ten users of one tenant sharing a budget is the fairness this is for. The
// existing per-IP limiters on the public /auth routes solve a different problem
// (an unauthenticated attacker) and neither subsumes the other.
type AccountRateLimiter struct {
	rps    rate.Limit
	burst  int
	logger *slog.Logger

	mu          sync.Mutex
	buckets     map[string]*accountBucket
	lastSweep   time.Time
	nowOverride func() time.Time // tests only
}

// NewAccountRateLimiter builds a limiter. A non-positive rate or burst disables
// it entirely and Middleware becomes a pass-through, following the repo's quota
// convention (0 = unlimited) so an operator can switch it off without a rebuild.
func NewAccountRateLimiter(rps float64, burst int, logger *slog.Logger) *AccountRateLimiter {
	return &AccountRateLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		logger:  logger.With("component", "account_rate_limit"),
		buckets: make(map[string]*accountBucket),
	}
}

// Enabled reports whether the limiter does anything.
func (l *AccountRateLimiter) Enabled() bool { return l.rps > 0 && l.burst > 0 }

func (l *AccountRateLimiter) now() time.Time {
	if l.nowOverride != nil {
		return l.nowOverride()
	}
	return time.Now()
}

// Middleware returns the Echo middleware.
//
// It MUST be composed inside the JWT middleware, not registered with e.Use.
// Echo applies e.Use middleware to the router-resolved handler, i.e. BEFORE any
// group or route middleware, so a limiter registered that way would run ahead of
// JWTAuth, find no account in the context, and silently degrade to one shared
// global bucket — which is worse than nothing, because it looks like it works
// while making every tenant share one budget. This is the same trap the 2FA
// enrollment gate documents next to it in router.go.
func (l *AccountRateLimiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if !l.Enabled() {
			return next
		}
		return func(c echo.Context) error {
			accountID, err := GetAccountIDFromContext(c)
			if err != nil {
				// No account on the context. Either this middleware was wired
				// above the JWT one (see the doc comment) or the route is not
				// really authenticated; in both cases the safe behaviour is to
				// let the request through and let the real auth decide. A
				// limiter must never become the thing that answers 401.
				return next(c)
			}
			if l.allow(accountID.String()) {
				return next(c)
			}

			// A retry hint, because the caller most likely to hit this is a
			// script, and a script with no hint retries immediately.
			retryAfter := 1
			c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))
			l.logger.WarnContext(c.Request().Context(), "account API rate limit exceeded",
				"account_id", accountID, "path", c.Path(), "method", c.Request().Method)
			return echo.NewHTTPError(http.StatusTooManyRequests,
				"too many requests for this account; slow down and retry")
		}
	}
}

func (l *AccountRateLimiter) allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	l.sweepLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &accountBucket{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	limiter := b.limiter
	l.mu.Unlock()

	// AllowN with an explicit time, so a test can drive the clock instead of
	// sleeping — and so the bucket's notion of time matches the sweeper's.
	return limiter.AllowN(now, 1)
}

// sweepLocked drops buckets that have been idle. Called with l.mu held.
func (l *AccountRateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < bucketSweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > bucketIdleTTL {
			delete(l.buckets, k)
		}
	}
}
