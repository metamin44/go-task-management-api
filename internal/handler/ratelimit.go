package handler

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// Conservative defaults for the per-IP authentication rate limiter. The
// sustained allowance is one request per interval per IP, with a small burst.
const (
	defaultAuthRateInterval = time.Second
	authRateBurst           = 5
	authRateMaxEntries      = 10000 // bounded memory
	authRateEvictSample     = 16    // bounded-cost eviction sample size
)

// authRateLimiter is a concurrency-safe, per-IP token bucket with lazy refill
// and bounded memory. It keys on the connection peer address (c.RealIP with
// no proxy extractor configured), so client-supplied forwarding headers can
// neither spoof nor evade the limit. Limits are per process instance: a
// multi-replica deployment must also enforce limits at a reverse proxy or
// shared gateway.
type authRateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	burst    int
	idleTTL  time.Duration
	max      int
	entries  map[string]*rateEntry
	now      func() time.Time // injectable clock for deterministic tests
}

type rateEntry struct {
	tokens float64
	last   time.Time
}

// newAuthRateLimiter builds a limiter; a non-positive interval falls back to
// the conservative default.
func newAuthRateLimiter(interval time.Duration) *authRateLimiter {
	if interval <= 0 {
		interval = defaultAuthRateInterval
	}
	return &authRateLimiter{
		interval: interval,
		burst:    authRateBurst,
		idleTTL:  10 * interval,
		max:      authRateMaxEntries,
		entries:  make(map[string]*rateEntry),
		now:      time.Now,
	}
}

// allow reports whether ip may proceed now and, when denied, how long until a
// token is available. Tokens refill lazily at one per interval up to burst.
func (l *authRateLimiter) allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e, ok := l.entries[ip]
	if !ok {
		l.evictSampled(now)
		e = &rateEntry{tokens: float64(l.burst)}
		l.entries[ip] = e
	}
	if elapsed := now.Sub(e.last); elapsed > 0 {
		e.tokens = math.Min(float64(l.burst), e.tokens+float64(elapsed)/float64(l.interval))
	}
	e.last = now
	if e.tokens >= 1 {
		e.tokens--
		return true, 0
	}
	return false, time.Duration((1 - e.tokens) * float64(l.interval))
}

// evictSampled bounds memory with bounded-cost eviction. When at capacity it
// inspects up to authRateEvictSample entries (Go map iteration order is
// randomized, giving a sample), evicting a stale entry if one is sampled,
// otherwise the oldest sampled entry. This is O(sample) per insertion rather
// than a full-map scan, so a high-cardinality IP flood cannot amplify CPU on
// the request path. The hard cap is preserved: one entry is removed before each
// insertion at capacity. Called with l.mu held.
func (l *authRateLimiter) evictSampled(now time.Time) {
	if len(l.entries) < l.max {
		return
	}
	var oldestIP string
	var oldest time.Time
	haveOldest := false
	n := 0
	for ip, e := range l.entries {
		if now.Sub(e.last) > l.idleTTL {
			delete(l.entries, ip) // prefer reclaiming an idle entry
			return
		}
		if !haveOldest || e.last.Before(oldest) {
			oldestIP, oldest, haveOldest = ip, e.last, true
		}
		if n++; n >= authRateEvictSample {
			break
		}
	}
	if haveOldest {
		delete(l.entries, oldestIP)
	}
}

// limitAuth wraps an expensive authentication handler with the per-IP limiter.
// Denied requests get HTTP 429 with the standard error envelope and a
// Retry-After header (whole seconds, at least 1).
func (a *API) limitAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if a.authLimiter == nil {
			return next(c)
		}
		if ok, retry := a.authLimiter.allow(c.RealIP()); !ok {
			secs := int(math.Ceil(retry.Seconds()))
			if secs < 1 {
				secs = 1
			}
			c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
			return &apiError{http.StatusTooManyRequests, "rate_limited", "too many requests; retry later"}
		}
		return next(c)
	}
}
