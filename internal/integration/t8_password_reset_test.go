//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
	"github.com/metamin44/go-task-management-api/internal/service"
)

// T8 — password reset against real PostgreSQL: hash-only storage, expiry,
// single-use, concurrent single-winner, session revocation, and FK cascade.

func seedReset(t *testing.T, f *fixture, uid domain.UUID, raw string, expires time.Time) {
	t.Helper()
	sum := sha256.Sum256([]byte(raw))
	tok := &domain.PasswordResetToken{
		ID: domain.NewUUID(), UserID: uid, TokenHash: sum[:], ExpiresAt: expires,
	}
	if expires.After(time.Now()) {
		// Normal issuance path: production Create (DB fills created_at = now()).
		if err := f.resets.Create(context.Background(), tok); err != nil {
			t.Fatalf("seed reset token: %v", err)
		}
		return
	}
	// An already-expired token cannot be inserted via Create: the schema enforces
	// CHECK (expires_at > created_at) and Create lets the DB default created_at to
	// now(), so a past expires_at would violate that invariant (23514 → invalid
	// input). Craft the expired row directly with created_at safely before
	// expires_at (both in the past). Test setup only — production validation is
	// unchanged, and the token is still expired relative to now().
	createdAt := expires.Add(-time.Minute)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		tok.ID, tok.UserID, tok.TokenHash, tok.ExpiresAt, createdAt); err != nil {
		t.Fatalf("seed expired reset token: %v", err)
	}
}

func TestResetForgotStoresHashOnlyAndMailsCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "alice@example.com")

	if err := f.resetSvc.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatalf("forgot password: %v", err)
	}
	call := f.mailer.nextCall(t)
	raw := call.code
	// S-05: the page URL is the clean base; the code is separate, never URL-borne.
	if call.pageURL != "https://app.example.com/reset" {
		t.Errorf("page URL must be the clean base, got %q", call.pageURL)
	}
	if strings.Contains(call.pageURL, raw) || strings.Contains(call.pageURL, "token") {
		t.Errorf("page URL must not carry the code: %q", call.pageURL)
	}

	var stored []byte
	var consumed *time.Time
	if err := f.pool.QueryRow(ctx,
		"SELECT token_hash, consumed_at FROM password_reset_tokens WHERE user_id = $1", uid).
		Scan(&stored, &consumed); err != nil {
		t.Fatalf("read token row: %v", err)
	}
	sum := sha256.Sum256([]byte(raw))
	if !bytes.Equal(stored, sum[:]) {
		t.Error("stored hash must equal SHA-256 of the mailed raw code")
	}
	if bytes.Equal(stored, []byte(raw)) {
		t.Error("raw code must never be stored")
	}
	if consumed != nil {
		t.Error("new token must be unconsumed")
	}

	// Unknown email creates no row and sends no mail (enumeration-safe).
	before := countRows(t, f, "password_reset_tokens")
	if err := f.resetSvc.ForgotPassword(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("unknown email must succeed generically: %v", err)
	}
	if after := countRows(t, f, "password_reset_tokens"); after != before {
		t.Errorf("unknown email must not create a token: before=%d after=%d", before, after)
	}
}

func TestResetPasswordSuccessRevokesSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pair, uid := loginPair(t, f, "alice@example.com")
	if _, err := f.auth.Login(ctx, "alice@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	seedReset(t, f, uid, "success-reset-token", time.Now().Add(15*time.Minute))

	if err := f.resetSvc.ResetPassword(ctx, "success-reset-token", "newpassword456"); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	var consumed *time.Time
	if err := f.pool.QueryRow(ctx,
		"SELECT consumed_at FROM password_reset_tokens WHERE token_hash = $1", tokenHash("success-reset-token")).
		Scan(&consumed); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if consumed == nil {
		t.Error("token must be consumed")
	}
	if n := countActive(t, f, sessionIDByHash(t, f, pair.RefreshToken)); n != 0 {
		t.Errorf("all sessions must be revoked, %d still active", n)
	}
	if _, err := f.auth.Login(ctx, "alice@example.com", "newpassword456"); err != nil {
		t.Errorf("new password must authenticate: %v", err)
	}
	if _, err := f.auth.Login(ctx, "alice@example.com", "password123"); !errors.Is(err, service.ErrUnauthorized) {
		t.Errorf("old password must be rejected, got %v", err)
	}
}

func TestResetExpiredTokenRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, uid := loginPair(t, f, "alice@example.com")
	seedReset(t, f, uid, "expired-reset-token", time.Now().Add(-time.Minute))

	if err := f.resetSvc.ResetPassword(ctx, "expired-reset-token", "newpassword456"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("expired token must be unauthorized, got %v", err)
	}
	if _, err := f.auth.Login(ctx, "alice@example.com", "password123"); err != nil {
		t.Errorf("expired token must not change the password: %v", err)
	}
}

func TestResetReusedTokenRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, uid := loginPair(t, f, "alice@example.com")
	seedReset(t, f, uid, "single-use-reset-token", time.Now().Add(15*time.Minute))

	if err := f.resetSvc.ResetPassword(ctx, "single-use-reset-token", "newpassword456"); err != nil {
		t.Fatalf("first use must succeed: %v", err)
	}
	if err := f.resetSvc.ResetPassword(ctx, "single-use-reset-token", "anotherpassword789"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("reused token must be unauthorized, got %v", err)
	}
}

func TestResetUnknownTokenRejected(t *testing.T) {
	f := newFixture(t)
	seedUser(t, f, "alice@example.com")
	if err := f.resetSvc.ResetPassword(context.Background(), "no-such-token", "newpassword456"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatalf("unknown token must be unauthorized, got %v", err)
	}
}

func TestResetConcurrentSingleWinner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, uid := loginPair(t, f, "alice@example.com")
	const raw = "concurrent-reset-token"
	seedReset(t, f, uid, raw, time.Now().Add(15*time.Minute))

	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := f.resetSvc.ResetPassword(context.Background(), raw, "newpassword456")
			errs[i] = err
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if success != 1 {
		t.Fatalf("exactly one concurrent reset must succeed, got %d", success)
	}
	for _, err := range errs {
		if err != nil && !errors.Is(err, service.ErrUnauthorized) {
			t.Errorf("losers must be unauthorized, got %v", err)
		}
	}
	var consumed *time.Time
	if err := f.pool.QueryRow(ctx,
		"SELECT consumed_at FROM password_reset_tokens WHERE token_hash = $1", tokenHash(raw)).
		Scan(&consumed); err != nil || consumed == nil {
		t.Errorf("token must be consumed exactly once: consumed=%v err=%v", consumed, err)
	}
	if _, err := f.auth.Login(ctx, "alice@example.com", "newpassword456"); err != nil {
		t.Errorf("winner's new password must authenticate: %v", err)
	}
}

func TestResetTokensCascadeOnUserDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := seedUser(t, f, "alice@example.com")
	seedReset(t, f, uid, "cascade-reset-token", time.Now().Add(15*time.Minute))

	if err := f.users.Delete(ctx, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if n := countRows(t, f, "password_reset_tokens"); n != 0 {
		t.Errorf("reset tokens must cascade on user delete, %d remain", n)
	}
}
