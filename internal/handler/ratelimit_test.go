package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// ---- limiter unit tests (deterministic injected clock) ----

func TestRateLimiterBurstDenyRecover(t *testing.T) {
	l := newAuthRateLimiter(time.Second)
	clock := time.Unix(1000, 0)
	l.now = func() time.Time { return clock }

	for i := 0; i < authRateBurst; i++ {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("request %d within burst must pass", i)
		}
	}
	ok, retry := l.allow("1.2.3.4")
	if ok {
		t.Fatal("request beyond burst must be denied")
	}
	if retry <= 0 || retry > time.Second {
		t.Fatalf("retry-after out of range: %v", retry)
	}
	// One interval later exactly one token refills.
	clock = clock.Add(time.Second)
	if ok, _ := l.allow("1.2.3.4"); !ok {
		t.Fatal("must recover after one interval")
	}
	if ok, _ := l.allow("1.2.3.4"); ok {
		t.Fatal("only one token should have refilled")
	}
}

func TestRateLimiterConcurrentSameIP(t *testing.T) {
	l := newAuthRateLimiter(time.Hour) // no refill during the test
	frozen := time.Unix(2000, 0)
	l.now = func() time.Time { return frozen }

	const n = 200
	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _ := l.allow("9.9.9.9"); ok {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := allowed.Load(); got != int64(authRateBurst) {
		t.Fatalf("concurrent allows = %d, want exactly %d", got, authRateBurst)
	}
}

func TestRateLimiterIndependentIPs(t *testing.T) {
	l := newAuthRateLimiter(time.Hour)
	l.now = func() time.Time { return time.Unix(3000, 0) }
	for i := 0; i < authRateBurst; i++ {
		l.allow("10.0.0.1")
	}
	if ok, _ := l.allow("10.0.0.1"); ok {
		t.Fatal("first IP must be exhausted")
	}
	if ok, _ := l.allow("10.0.0.2"); !ok {
		t.Fatal("a different IP must have its own bucket")
	}
}

func TestRateLimiterStaleCleanupBoundsMemory(t *testing.T) {
	l := newAuthRateLimiter(time.Second)
	l.max = 50
	clock := time.Unix(4000, 0)
	l.now = func() time.Time { return clock }
	for i := 0; i < 50; i++ {
		l.allow(fmt.Sprintf("ip-%d", i))
	}
	if len(l.entries) != 50 {
		t.Fatalf("entries = %d, want 50", len(l.entries))
	}
	// Advance beyond idleTTL (10s): every entry is stale. Inserting a new IP
	// must reclaim a stale entry (sampled) and stay within the hard cap.
	clock = clock.Add(11 * time.Second)
	l.allow("fresh-ip")
	if len(l.entries) > 50 {
		t.Fatalf("memory not bounded: %d entries", len(l.entries))
	}
	if _, ok := l.entries["fresh-ip"]; !ok {
		t.Error("fresh entry must be present")
	}
	remaining := 0
	for ip := range l.entries {
		if ip != "fresh-ip" {
			remaining++
		}
	}
	if remaining >= 50 {
		t.Errorf("expected a stale entry to be evicted, %d old entries remain", remaining)
	}
}

func TestRateLimiterEvictsWhenFullNotStale(t *testing.T) {
	l := newAuthRateLimiter(time.Second)
	l.max = 10
	clock := time.Unix(5000, 0)
	l.now = func() time.Time { return clock }
	for i := 0; i < 10; i++ {
		l.allow(fmt.Sprintf("ip-%d", i))
		clock = clock.Add(time.Millisecond) // all within idleTTL
	}
	l.allow("fresh")
	if len(l.entries) > 10 {
		t.Fatalf("memory not bounded: %d entries", len(l.entries))
	}
	if _, ok := l.entries["fresh"]; !ok {
		t.Error("fresh entry must be present after eviction")
	}
}

// Q-02: with capacity at or below the eviction sample size (authRateEvictSample
// = 16), the sample exhausts the map, so eviction is fully deterministic using
// only the injected clock — no production change or artificial determinism.
// When no entry is stale, the OLDEST entry must be evicted.
func TestRateLimiterSampledEvictionOldestWhenNoneStale(t *testing.T) {
	l := newAuthRateLimiter(time.Second)
	l.max = 4 // <= authRateEvictSample: the sample covers every entry
	clock := time.Unix(7000, 0)
	l.now = func() time.Time { return clock }

	for _, ip := range []string{"ip-0", "ip-1", "ip-2", "ip-3"} {
		l.allow(ip)
		clock = clock.Add(time.Millisecond) // staggered, all within idleTTL
	}
	// No entry is stale: inserting a 5th IP must evict the oldest (ip-0).
	l.allow("fresh")
	if len(l.entries) != 4 {
		t.Fatalf("entries = %d, want exactly 4", len(l.entries))
	}
	if _, ok := l.entries["ip-0"]; ok {
		t.Error("oldest entry ip-0 must be evicted when none are stale")
	}
	for _, ip := range []string{"ip-1", "ip-2", "ip-3", "fresh"} {
		if _, ok := l.entries[ip]; !ok {
			t.Errorf("entry %s must survive", ip)
		}
	}
}

// Q-02: a stale entry is preferred over fresh ones. With capacity <= sample
// size the stale entry is guaranteed to be sampled, so this is deterministic.
func TestRateLimiterSampledEvictionPrefersStale(t *testing.T) {
	l := newAuthRateLimiter(time.Second)
	l.max = 4
	clock := time.Unix(8000, 0)
	l.now = func() time.Time { return clock }
	for _, ip := range []string{"ip-0", "ip-1", "ip-2", "ip-3"} {
		l.allow(ip)
	}
	// Advance past idleTTL (10s) and refresh three entries so ONLY ip-0 is stale.
	clock = clock.Add(11 * time.Second)
	for _, ip := range []string{"ip-1", "ip-2", "ip-3"} {
		l.allow(ip) // updates last to now; burst tokens still available
	}
	l.allow("fresh")
	if _, ok := l.entries["ip-0"]; ok {
		t.Error("the stale entry ip-0 must be evicted in preference to fresh ones")
	}
	if len(l.entries) != 4 {
		t.Fatalf("entries = %d, want exactly 4", len(l.entries))
	}
	for _, ip := range []string{"ip-1", "ip-2", "ip-3", "fresh"} {
		if _, ok := l.entries[ip]; !ok {
			t.Errorf("entry %s must survive", ip)
		}
	}
}

// R-02: a high-cardinality flood of distinct IPs must never exceed the hard cap,
// even under concurrent access (eviction is bounded-cost and mutex-guarded).
func TestRateLimiterCapacityUnderConcurrency(t *testing.T) {
	l := newAuthRateLimiter(time.Hour)
	l.max = 100
	l.now = func() time.Time { return time.Unix(6000, 0) }
	const n = 500
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			l.allow(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
		}(i)
	}
	close(start)
	wg.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) > l.max {
		t.Fatalf("capacity violated under concurrency: %d > %d", len(l.entries), l.max)
	}
	// Q-02: 500 distinct IPs against a 100 cap must saturate it exactly — every
	// insertion at capacity evicts precisely one entry, so len == max.
	if len(l.entries) != l.max {
		t.Fatalf("expected exact saturation: %d entries, want %d", len(l.entries), l.max)
	}
}

// ---- handler-level tests (real Echo routing + middleware) ----

// serveAuth builds a request with a controlled peer address and optional
// forwarding headers, then serves it through the full middleware stack.
func serveAuth(env *testEnv, method, path, body, token, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

func loginEnv(t *testing.T, interval time.Duration) *testEnv {
	t.Helper()
	env := newTestEnv(t, Options{}).withRateLimit(interval)
	env.auth.loginFn = func(context.Context, string, string) (*service.TokenPair, error) {
		return &service.TokenPair{AccessToken: "a", RefreshToken: "r"}, nil
	}
	return env
}

const loginBody = `{"email":"e@x.y","password":"password123"}`

func TestAuthRateLimitEnforcedWithRetryAfter(t *testing.T) {
	env := loginEnv(t, time.Hour) // no refill during the test
	for i := 0; i < authRateBurst; i++ {
		rec := serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.1:1000", nil)
		assertStatus(t, rec, http.StatusOK)
	}
	rec := serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.1:1000", nil)
	assertStatus(t, rec, http.StatusTooManyRequests)
	if decodeEnvelope(t, rec).Error.Code != "rate_limited" {
		t.Error("expected code rate_limited")
	}
	ra := rec.Header().Get("Retry-After")
	n, err := strconv.Atoi(ra)
	if err != nil || n < 1 {
		t.Fatalf("Retry-After must be a positive integer of seconds, got %q", ra)
	}
	assertNoLeak(t, rec.Body.String(), env.logs.String(), loginBody)
}

func TestAuthRateLimitIgnoresForwardingHeaders(t *testing.T) {
	env := loginEnv(t, time.Hour)
	// Same peer, spoofed forwarding headers must not evade the limit.
	for i := 0; i < authRateBurst; i++ {
		rec := serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.7:2000",
			map[string]string{"X-Forwarded-For": fmt.Sprintf("10.0.0.%d", i), "X-Real-IP": fmt.Sprintf("10.1.1.%d", i)})
		assertStatus(t, rec, http.StatusOK)
	}
	rec := serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.7:2000",
		map[string]string{"X-Forwarded-For": "10.9.9.9"})
	assertStatus(t, rec, http.StatusTooManyRequests)
	// A genuinely different peer address gets its own bucket.
	rec = serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.8:2000", nil)
	assertStatus(t, rec, http.StatusOK)
}

func TestAuthRateLimitRecoversAfterInterval(t *testing.T) {
	env := loginEnv(t, 10*time.Millisecond)
	for i := 0; i < authRateBurst; i++ {
		serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.9:3000", nil)
	}
	rec := serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.9:3000", nil)
	assertStatus(t, rec, http.StatusTooManyRequests)
	time.Sleep(60 * time.Millisecond) // comfortably beyond the refill interval
	rec = serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.9:3000", nil)
	assertStatus(t, rec, http.StatusOK)
}

func TestAuthRateLimitShortCircuitsService(t *testing.T) {
	env := newTestEnv(t, Options{}).withRateLimit(time.Hour)
	var calls atomic.Int64
	env.auth.loginFn = func(context.Context, string, string) (*service.TokenPair, error) {
		calls.Add(1)
		return &service.TokenPair{AccessToken: "a", RefreshToken: "r"}, nil
	}
	for i := 0; i < authRateBurst+3; i++ {
		serveAuth(env, http.MethodPost, "/api/v1/auth/login", loginBody, "", "192.0.2.10:4000", nil)
	}
	if got := calls.Load(); got != int64(authRateBurst) {
		t.Fatalf("service invoked %d times, want %d (denied requests must not reach it)", got, authRateBurst)
	}
}

func TestAuthRateLimitCoversRefreshAndPassword(t *testing.T) {
	env := newTestEnv(t, Options{}).withRateLimit(time.Hour)
	env.auth.refreshFn = func(context.Context, string) (*service.TokenPair, error) {
		return &service.TokenPair{AccessToken: "a", RefreshToken: "r"}, nil
	}
	env.auth.changePasswordFn = func(context.Context, domain.UUID, string, string) error { return nil }

	for i := 0; i < authRateBurst; i++ {
		rec := serveAuth(env, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"r"}`, "", "192.0.2.20:5000", nil)
		assertStatus(t, rec, http.StatusOK)
	}
	rec := serveAuth(env, http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"r"}`, "", "192.0.2.20:5000", nil)
	assertStatus(t, rec, http.StatusTooManyRequests)

	pwBody := `{"current_password":"password123","new_password":"password456"}`
	for i := 0; i < authRateBurst; i++ {
		rec := serveAuth(env, http.MethodPatch, "/api/v1/users/me/password", pwBody, testToken, "192.0.2.21:5000", nil)
		assertStatus(t, rec, http.StatusNoContent)
	}
	rec = serveAuth(env, http.MethodPatch, "/api/v1/users/me/password", pwBody, testToken, "192.0.2.21:5000", nil)
	assertStatus(t, rec, http.StatusTooManyRequests)
}
