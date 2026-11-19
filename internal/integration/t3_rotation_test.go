//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// T3 (PROJECT_CONTEXT.md §8): N≈16 concurrent rotations of the same raw
// token via the real service → exactly one success, others take the reuse
// path; final state: zero active rows for the session; R1.replaced_by =
// R2.id; no unretried deadlocks.
func TestT3ConcurrentRotationSingleWinner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pair, _ := loginPair(t, f, "user@example.com")

	const n = 16
	results := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // barrier: maximize real contention on the row lock
			_, results[i] = f.auth.Refresh(ctx, pair.RefreshToken)
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for i, err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, service.ErrTokenReused) {
			t.Errorf("goroutine %d: loser must take the reuse path (or retryable errors must be retried), got %v", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one rotation may succeed, got %d", successes)
	}

	sid := sessionIDByHash(t, f, pair.RefreshToken)
	if n := countActive(t, f, sid); n != 0 {
		t.Errorf("reuse revocation must leave zero active rows, got %d", n)
	}
	// Exactly two rows: original + the winner's replacement (losers insert
	// nothing). R1.replaced_by must point at R2 within the same session.
	var total int64
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM refresh_sessions WHERE session_id = $1", sid).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("expected exactly 2 chain rows, got %d", total)
	}
	var replacedBy *domain.UUID
	if err := f.pool.QueryRow(ctx,
		"SELECT replaced_by FROM refresh_sessions WHERE token_hash = $1", tokenHash(pair.RefreshToken)).Scan(&replacedBy); err != nil {
		t.Fatal(err)
	}
	if replacedBy == nil {
		t.Fatal("original row must link to its replacement")
	}
	var linked int64
	if err := f.pool.QueryRow(ctx,
		"SELECT count(*) FROM refresh_sessions WHERE id = $1 AND session_id = $2", *replacedBy, sid).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 1 {
		t.Errorf("R1.replaced_by must equal R2.id in the same session")
	}
}

// T3 variant: two sessions of one user rotate independently and concurrently.
func TestT3IndependentSessionsRotateConcurrently(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pair1, _ := loginPair(t, f, "user@example.com")
	pair2, err := f.auth.Login(ctx, "user@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		pair *service.TokenPair
		err  error
	}
	results := make([]outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, raw := range []string{pair1.RefreshToken, pair2.RefreshToken} {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			<-start
			p, err := f.auth.Refresh(ctx, raw)
			results[i] = outcome{p, err}
		}(i, raw)
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("session %d must rotate independently: %v", i+1, r.err)
		}
	}
	for _, raw := range []string{pair1.RefreshToken, pair2.RefreshToken} {
		sid := sessionIDByHash(t, f, raw)
		if n := countActive(t, f, sid); n != 1 {
			t.Errorf("each session keeps exactly one active row, got %d", n)
		}
	}
}

// T3 variant: expired and unknown tokens → 401; expiry revocation commits.
func TestT3ExpiredAndUnknownTokens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "user@example.com")

	// Expired fixture via direct SQL: created_at must precede expires_at
	// (DB CHECK), so both timestamps are aged in the PG clock domain.
	raw := "expired-refresh-token"
	id, sid := domain.NewUUID(), domain.NewUUID()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO refresh_sessions (id, session_id, user_id, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, now() - interval '1 day', now() - interval '2 days')`,
		id, sid, uid, tokenHash(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.Refresh(ctx, raw); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("expired token must be unauthorized, got %v", err)
	}
	var revoked bool
	if err := f.pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM refresh_sessions WHERE id = $1", id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("expiry revocation must be committed despite the failed request")
	}

	if _, err := f.auth.Refresh(ctx, "unknown-refresh-token"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("unknown token must be unauthorized, got %v", err)
	}
}

// T3 variant: replaying an already-rotated token revokes the whole session,
// including the newer active token.
func TestT3ChainReplayRevokesSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pair1, _ := loginPair(t, f, "user@example.com")

	pair2, err := f.auth.Refresh(ctx, pair1.RefreshToken)
	if err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	if _, err := f.auth.Refresh(ctx, pair1.RefreshToken); !errors.Is(err, service.ErrTokenReused) {
		t.Fatalf("chain replay must be detected, got %v", err)
	}
	sid := sessionIDByHash(t, f, pair1.RefreshToken)
	if n := countActive(t, f, sid); n != 0 {
		t.Errorf("replay must revoke the whole session, active=%d", n)
	}
	if _, err := f.auth.Refresh(ctx, pair2.RefreshToken); !errors.Is(err, service.ErrTokenReused) {
		t.Fatalf("the newer token must also be dead after replay revocation, got %v", err)
	}
}
