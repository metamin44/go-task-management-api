package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

func TestRegister(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)

	u, err := s.Register(ctx, "Alice", "Alice@Example.com", "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email not normalized: %q", u.Email)
	}
	if u.PasswordHash != "" {
		t.Error("returned user must not contain the password hash")
	}
	stored := f.db.users[u.ID]
	if stored.PasswordHash == "password123" || stored.PasswordHash == "" {
		t.Error("password must be stored hashed, never plaintext")
	}

	if _, err := s.Register(ctx, "Other", "ALICE@example.com", "password123"); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate email must conflict, got %v", err)
	}
	for _, tc := range []struct{ name, email, pw string }{
		{"", "a@b.com", "password123"},
		{"   ", "a@b.com", "password123"},
		{"Bob", "invalid", "password123"},
		{"Bob", "b@b.com", "short"},
	} {
		if _, err := s.Register(ctx, tc.name, tc.email, tc.pw); err == nil {
			t.Errorf("invalid input accepted: %+v", tc)
		}
	}
}

func TestLogin(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	if _, err := s.Register(ctx, "Alice", "alice@example.com", "password123"); err != nil {
		t.Fatal(err)
	}

	pair, err := s.Login(ctx, "ALICE@example.com", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("login must issue both tokens")
	}
	// Stored hash must equal SHA-256 of the presented raw token.
	sum := sha256.Sum256([]byte(pair.RefreshToken))
	var found bool
	for _, rs := range f.db.sessions {
		if bytes.Equal(rs.TokenHash, sum[:]) {
			found = true
			if rs.RevokedAt != nil {
				t.Error("new login session must be active")
			}
			if !rs.ExpiresAt.After(time.Now()) {
				t.Error("new refresh token must expire in the future")
			}
		}
	}
	if !found {
		t.Error("refresh token must be stored only as its SHA-256 hash")
	}

	if _, err := s.Login(ctx, "alice@example.com", "wrong-password"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong password must be unauthorized, got %v", err)
	}
	if _, err := s.Login(ctx, "nobody@example.com", "password123"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown email must be unauthorized, got %v", err)
	}
}

func activeSessionCount(f *fakeStore, sessionID domain.UUID) int {
	n := 0
	for _, rs := range f.db.sessions {
		if rs.SessionID == sessionID && rs.RevokedAt == nil {
			n++
		}
	}
	return n
}

func sessionOfRaw(f *fakeStore, raw string) domain.RefreshSession {
	sum := sha256.Sum256([]byte(raw))
	for _, rs := range f.db.sessions {
		if bytes.Equal(rs.TokenHash, sum[:]) {
			return rs
		}
	}
	panic("session for raw token not found")
}

func TestRefreshRotation(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	uid := seedUser(f, "alice@example.com")
	pair, err := s.Login(ctx, "alice@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	old := sessionOfRaw(f, pair.RefreshToken)

	f.log = nil // isolate the rotation's operation sequence
	newPair, err := s.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	newSess := sessionOfRaw(f, newPair.RefreshToken)

	if newSess.SessionID != old.SessionID {
		t.Error("rotation must keep the same session_id")
	}
	if newSess.UserID != uid {
		t.Error("rotation must keep the same user")
	}
	updatedOld := f.db.sessions[old.ID]
	if updatedOld.RevokedAt == nil {
		t.Error("old token must be revoked")
	}
	if updatedOld.ReplacedBy == nil || *updatedOld.ReplacedBy != newSess.ID {
		t.Error("old token must link replaced_by to the new row")
	}
	if activeSessionCount(f, old.SessionID) != 1 {
		t.Error("exactly one active token per session after rotation")
	}
	// Ordering: MarkReplaced must precede the new row's Create (deferred FK).
	mr, cr := f.logIndex("sessions.MarkReplaced"), f.logIndex("sessions.Create")
	if mr == -1 || cr == -1 || mr > cr {
		t.Errorf("MarkReplaced must run before Create; log=%v", f.log)
	}
	claims, err := s.ValidateAccessToken(newPair.AccessToken, time.Now())
	if err != nil || claims.UserID != uid {
		t.Errorf("new access token must validate for the user: %v", err)
	}
}

func TestRefreshReplayRevokesSession(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	seedUser(f, "alice@example.com")
	pair, err := s.Login(ctx, "alice@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	old := sessionOfRaw(f, pair.RefreshToken)
	if _, err := s.Refresh(ctx, pair.RefreshToken); err != nil {
		t.Fatal(err)
	}

	_, err = s.Refresh(ctx, pair.RefreshToken) // replay of the rotated token
	if !errors.Is(err, ErrTokenReused) {
		t.Fatalf("replay must return ErrTokenReused, got %v", err)
	}
	if activeSessionCount(f, old.SessionID) != 0 {
		t.Error("replay must revoke the entire session, including the newest token")
	}
	// Revocation must be COMMITTED despite the failed request.
	if f.logIndex("rollback") != -1 && f.log[len(f.log)-1] == "rollback" {
		t.Error("reuse-path revocation must commit, not roll back")
	}
}

func TestRefreshExpired(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	uid := seedUser(f, "alice@example.com")
	raw, hash, err := newRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	sid, id := domain.NewUUID(), domain.NewUUID()
	f.db.sessions[id] = domain.RefreshSession{
		ID: id, SessionID: sid, UserID: uid, TokenHash: hash,
		ExpiresAt: time.Now().Add(-time.Minute), CreatedAt: time.Now().Add(-time.Hour),
	}
	if _, err := s.Refresh(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired token must be unauthorized, got %v", err)
	}
	if f.db.sessions[id].RevokedAt == nil {
		t.Error("expired token must be marked revoked (committed)")
	}
}

func TestRefreshUnknown(t *testing.T) {
	f := newFakeStore()
	s := newTestAuthService(f)
	if _, err := s.Refresh(context.Background(), "unknown-token"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token must be unauthorized, got %v", err)
	}
}

func TestLogoutAndLogoutAll(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	uid := seedUser(f, "alice@example.com")
	p1, _ := s.Login(ctx, "alice@example.com", "password123")
	p2, _ := s.Login(ctx, "alice@example.com", "password123")

	if err := s.Logout(ctx, p1.RefreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	s1 := sessionOfRaw(f, p1.RefreshToken)
	if activeSessionCount(f, s1.SessionID) != 0 {
		t.Error("logout must revoke the session")
	}
	if err := s.Logout(ctx, p1.RefreshToken); err != nil {
		t.Errorf("logout must be idempotent: %v", err)
	}
	if err := s.Logout(ctx, "unknown"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown token logout must be unauthorized, got %v", err)
	}

	n, err := s.LogoutAll(ctx, uid)
	if err != nil || n != 1 {
		t.Fatalf("logout-all must revoke the remaining session: n=%d err=%v", n, err)
	}
	s2 := sessionOfRaw(f, p2.RefreshToken)
	if activeSessionCount(f, s2.SessionID) != 0 {
		t.Error("logout-all must revoke every session")
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	uid := seedUser(f, "alice@example.com")
	if _, err := s.Login(ctx, "alice@example.com", "password123"); err != nil {
		t.Fatal(err)
	}

	if err := s.ChangePassword(ctx, uid, "wrong-current", "newpassword456"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong current password must be unauthorized, got %v", err)
	}
	if err := s.ChangePassword(ctx, uid, "password123", "short"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("weak new password must be invalid, got %v", err)
	}

	if err := s.ChangePassword(ctx, uid, "password123", "newpassword456"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	stored := f.db.users[uid]
	if stored.PasswordHash == "newpassword456" || stored.PasswordHash == "encoded:password123" {
		t.Error("new password must be stored as a fresh hash")
	}
	for _, rs := range f.db.sessions {
		if rs.UserID == uid && rs.RevokedAt == nil {
			t.Error("password change must revoke all refresh sessions")
		}
	}

	// Rollback: revocation failure must leave the old hash intact.
	f.revokeErr = errors.New("injected revocation failure")
	if err := s.ChangePassword(ctx, uid, "newpassword456", "anotherpassword789"); err == nil {
		t.Fatal("injected failure must propagate")
	}
	if f.db.users[uid].PasswordHash != stored.PasswordHash {
		t.Error("failed password change must roll back completely")
	}
}

func TestAccessAndRefreshTokensAreDistinct(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestAuthService(f)
	seedUser(f, "alice@example.com")
	pair, err := s.Login(ctx, "alice@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	// A refresh token must never validate as an access token.
	if _, err := s.ValidateAccessToken(pair.RefreshToken, time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Error("refresh token must be rejected by access-token validation")
	}
	// An access token must never rotate as a refresh token.
	if _, err := s.Refresh(ctx, pair.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Error("access token must be rejected by refresh rotation")
	}
}

// countingHasher spies on Hash/Verify call counts while delegating to the
// deterministic fake hasher, so tests can prove both login failure paths
// perform exactly one password verification (L-01 timing equalization).
type countingHasher struct {
	fakeHasher
	hashCalls   atomic.Int64
	verifyCalls atomic.Int64
}

func (h *countingHasher) Hash(pw string) (string, error) {
	h.hashCalls.Add(1)
	return h.fakeHasher.Hash(pw)
}

func (h *countingHasher) Verify(encoded, pw string) (bool, error) {
	h.verifyCalls.Add(1)
	return h.fakeHasher.Verify(encoded, pw)
}

// L-01: unknown-email and wrong-password logins must both perform exactly one
// Argon2id verification and return the same ErrUnauthorized. The dummy hash is
// generated once (first unknown login) and reused thereafter — never per login.
func TestLoginTimingEqualization(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	h := &countingHasher{}
	s, err := NewAuthService(&f.users, &f.sessions, f, h, fakeTokens{}, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seedUser(f, "alice@example.com") // stored hash "encoded:password123"

	// Unknown email: one Verify (against the dummy) + one Hash to build it once.
	h.hashCalls.Store(0)
	h.verifyCalls.Store(0)
	if _, err := s.Login(ctx, "nobody@example.com", "password123"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown email must be unauthorized, got %v", err)
	}
	if got := h.verifyCalls.Load(); got != 1 {
		t.Errorf("unknown email must perform exactly one Verify, got %d", got)
	}
	if got := h.hashCalls.Load(); got != 1 {
		t.Errorf("dummy hash must be generated once on first unknown login, got %d Hash calls", got)
	}

	// Second unknown email: dummy reused, NOT regenerated.
	h.hashCalls.Store(0)
	h.verifyCalls.Store(0)
	if _, err := s.Login(ctx, "other@example.com", "password123"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown email must be unauthorized, got %v", err)
	}
	if got := h.verifyCalls.Load(); got != 1 {
		t.Errorf("unknown email must still perform one Verify, got %d", got)
	}
	if got := h.hashCalls.Load(); got != 0 {
		t.Errorf("dummy hash must not be regenerated per login, got %d Hash calls", got)
	}

	// Wrong password: exactly one Verify, no dummy generation.
	h.hashCalls.Store(0)
	h.verifyCalls.Store(0)
	if _, err := s.Login(ctx, "alice@example.com", "wrong-password"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong password must be unauthorized, got %v", err)
	}
	if got := h.verifyCalls.Load(); got != 1 {
		t.Errorf("wrong password must perform exactly one Verify, got %d", got)
	}
	if got := h.hashCalls.Load(); got != 0 {
		t.Errorf("wrong password must not generate a dummy hash, got %d Hash calls", got)
	}

	// Correct password: one Verify, then a session is issued.
	h.verifyCalls.Store(0)
	pair, err := s.Login(ctx, "alice@example.com", "password123")
	if err != nil || pair == nil {
		t.Fatalf("valid login must succeed: %v", err)
	}
	if got := h.verifyCalls.Load(); got != 1 {
		t.Errorf("valid login must perform exactly one Verify, got %d", got)
	}
}

// TestLoginUnknownUserDummyHashIsValidArgon2id proves the lazily generated dummy
// is a real, valid PHC argon2id hash: Verify processes it without a format error
// (returning false for a non-matching password), so the unknown-email path does
// genuine Argon2id work comparable to the wrong-password path.
func TestLoginUnknownUserDummyHashIsValidArgon2id(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	h := NewArgon2idHasher()
	s, err := NewAuthService(&f.users, &f.sessions, f, h, fakeTokens{}, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "nobody@example.com", "password123"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown email must be unauthorized, got %v", err)
	}
	if s.dummyHash == "" {
		t.Fatal("dummy hash must have been generated")
	}
	ok, verr := h.Verify(s.dummyHash, "password123")
	if verr != nil {
		t.Fatalf("dummy hash must be a valid PHC argon2id encoding, Verify errored: %v", verr)
	}
	if ok {
		t.Error("dummy hash must not match the submitted password")
	}
}
