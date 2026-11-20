package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/domain"
)

// recordingMailer captures deliveries and signals each call so tests can
// synchronize deterministically (no sleeps).
type recordingMailer struct {
	mu    sync.Mutex
	calls []mailCall
	err   error
	done  chan struct{}
}

type mailCall struct{ to, pageURL, code string }

func newRecordingMailer() *recordingMailer {
	return &recordingMailer{done: make(chan struct{}, 8)}
}

func (m *recordingMailer) SendPasswordReset(ctx context.Context, to, pageURL, code string) error {
	m.mu.Lock()
	m.calls = append(m.calls, mailCall{to, pageURL, code})
	err := m.err
	m.mu.Unlock()
	// Non-blocking signal: wait() consumes one per delivery in the single-call
	// tests; the bounded-concurrency test drains many calls and relies on
	// count(), so a full buffer must never block the workflow.
	select {
	case m.done <- struct{}{}:
	default:
	}
	return err
}

func (m *recordingMailer) wait(t *testing.T) mailCall {
	t.Helper()
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
		t.Fatal("mailer was not called")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// panicMailer panics during delivery to exercise S-02 recovery. The panic value
// deliberately contains a secret-like substring that must never be logged.
type panicMailer struct{}

func (panicMailer) SendPasswordReset(context.Context, string, string, string) error {
	panic("smtp boom: secret-code-xyz")
}

func newTestResetService(f *fakeStore, mailer ResetMailer, log *slog.Logger) *PasswordResetService {
	s, err := NewPasswordResetService(&f.users, &f.resets, &f.sessions, f, fakeHasher{}, mailer, 15*time.Minute, "https://app.example.com/reset", log)
	if err != nil {
		panic(err)
	}
	// Run delivery workflows synchronously in tests for determinism (the fake
	// store is not concurrency-safe). Production uses a real goroutine.
	s.spawn = func(fn func()) { fn() }
	return s
}

func seedResetToken(f *fakeStore, uid domain.UUID, raw string, expires time.Time, consumed bool) domain.UUID {
	sum := sha256.Sum256([]byte(raw))
	id := domain.NewUUID()
	rt := domain.PasswordResetToken{
		ID: id, UserID: uid, TokenHash: sum[:],
		ExpiresAt: expires, CreatedAt: time.Now().Add(-time.Minute),
	}
	if consumed {
		now := time.Now()
		rt.ConsumedAt = &now
	}
	f.db.resets[id] = rt
	return id
}

func TestForgotPasswordKnownEmail(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	s := newTestResetService(f, m, nil)
	uid := seedUser(f, "alice@example.com")

	if err := s.ForgotPassword(ctx, "ALICE@example.com"); err != nil {
		t.Fatalf("forgot password: %v", err)
	}
	call := m.wait(t)
	if call.to != "alice@example.com" {
		t.Errorf("email sent to %q, want normalized alice@example.com", call.to)
	}
	// S-05: the page URL is the clean base; the code is separate, never in a URL.
	if call.pageURL != "https://app.example.com/reset" {
		t.Errorf("page URL must be the clean base, got %q", call.pageURL)
	}
	if strings.Contains(call.pageURL, "token") || strings.Contains(call.pageURL, call.code) {
		t.Errorf("page URL must not carry the token: %q", call.pageURL)
	}
	raw := call.code

	if len(f.db.resets) != 1 {
		t.Fatalf("expected one reset token, got %d", len(f.db.resets))
	}
	sum := sha256.Sum256([]byte(raw))
	for _, rt := range f.db.resets {
		if rt.UserID != uid {
			t.Error("token must belong to the user")
		}
		if !bytes.Equal(rt.TokenHash, sum[:]) {
			t.Error("stored hash must equal SHA-256 of the raw code")
		}
		if bytes.Contains(rt.TokenHash, []byte(raw)) {
			t.Error("raw code must never be stored")
		}
		if rt.ConsumedAt != nil {
			t.Error("new token must be unconsumed")
		}
		if !rt.ExpiresAt.After(time.Now()) || rt.ExpiresAt.After(time.Now().Add(16*time.Minute)) {
			t.Errorf("token must expire within ~15m, got %v", rt.ExpiresAt)
		}
	}
}

func TestForgotPasswordUnknownEmail(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	s := newTestResetService(f, m, nil)

	if err := s.ForgotPassword(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("unknown email must return nil (generic response), got %v", err)
	}
	if len(f.db.resets) != 0 {
		t.Error("no token must be created for an unknown email")
	}
	if m.count() != 0 {
		t.Error("no email must be sent for an unknown email")
	}
}

// S-04: the synchronous request path must perform no DB writes for a known
// email (issuance is deferred to the async workflow), and an unknown email must
// schedule no workflow at all — equal synchronous work, no timing oracle.
func TestForgotPasswordSynchronousPathDoesNoWrites(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	s := newTestResetService(f, m, nil)
	var captured []func()
	s.spawn = func(fn func()) { captured = append(captured, fn) } // defer execution
	seedUser(f, "alice@example.com")

	f.log = nil
	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, op := range f.log {
		switch op {
		case "begin", "commit", "rollback", "resets.Create", "resets.DeleteUnconsumedByUser":
			t.Errorf("synchronous path must not perform write op %q (log=%v)", op, f.log)
		}
	}
	if len(captured) != 1 {
		t.Fatalf("known email must schedule exactly one async workflow, got %d", len(captured))
	}
	if len(f.db.resets) != 0 {
		t.Error("no token must exist before the async workflow runs")
	}

	if err := s.ForgotPassword(ctx, "nobody@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 {
		t.Error("unknown email must not schedule async work")
	}

	captured[0]() // run the deferred workflow
	if len(f.db.resets) != 1 {
		t.Error("async workflow must issue one token")
	}
	if m.count() != 1 {
		t.Error("async workflow must deliver one email")
	}
}

// S-02: a panic during delivery must be recovered (ForgotPassword still returns
// nil) and logged as a static message that never echoes the panic value, code,
// or recipient.
func TestForgotPasswordDeliveryPanicRecovered(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	var logs bytes.Buffer
	s := newTestResetService(f, panicMailer{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	seedUser(f, "alice@example.com")

	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatalf("delivery panic must be recovered, got %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "panicked") {
		t.Errorf("expected a static panic log, got %q", out)
	}
	for _, secret := range []string{"smtp boom", "secret-code-xyz", "alice@example.com"} {
		if strings.Contains(out, secret) {
			t.Errorf("panic log must not leak %q", secret)
		}
	}
}

// S-07: concurrent deliveries are bounded; over-capacity requests are dropped
// with a warning (still generic success) and slots are reusable after drain.
func TestForgotPasswordDeliveryConcurrencyBounded(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	var logs bytes.Buffer
	s := newTestResetService(f, m, slog.New(slog.NewJSONHandler(&logs, nil)))
	var captured []func()
	s.spawn = func(fn func()) { captured = append(captured, fn) }
	seedUser(f, "alice@example.com")

	for i := 0; i < resetDeliveryConcurrency; i++ {
		if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if len(captured) != resetDeliveryConcurrency {
		t.Fatalf("expected %d scheduled workflows, got %d", resetDeliveryConcurrency, len(captured))
	}
	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != resetDeliveryConcurrency {
		t.Error("over-capacity request must not spawn a workflow")
	}
	if !strings.Contains(logs.String(), "capacity") {
		t.Error("expected a capacity warning")
	}
	for _, fn := range captured {
		fn() // drain, releasing slots
	}
	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != resetDeliveryConcurrency+1 {
		t.Error("a slot must be reusable after drain")
	}
	captured[len(captured)-1]()
}

func TestForgotPasswordSupersedesPreviousCode(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	s := newTestResetService(f, m, nil)
	seedUser(f, "alice@example.com")

	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	first := m.wait(t).code
	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	second := m.wait(t).code

	if first == second {
		t.Fatal("each request must issue a fresh code")
	}
	if len(f.db.resets) != 1 {
		t.Fatalf("previous unconsumed token must be superseded, got %d tokens", len(f.db.resets))
	}
	if err := s.ResetPassword(ctx, first, "newpassword456"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("superseded code must be unauthorized, got %v", err)
	}
}

func TestForgotPasswordMailerFailureIsLoggedSafely(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	m := newRecordingMailer()
	m.err = errors.New("smtp: injected delivery failure")
	var logs bytes.Buffer
	s := newTestResetService(f, m, slog.New(slog.NewJSONHandler(&logs, nil)))
	seedUser(f, "alice@example.com")

	if err := s.ForgotPassword(ctx, "alice@example.com"); err != nil {
		t.Fatalf("forgot password must still succeed generically: %v", err)
	}
	call := m.wait(t)
	out := logs.String()
	if !strings.Contains(out, "delivery failed") {
		t.Errorf("expected a secret-safe warning, got %q", out)
	}
	for _, secret := range []string{call.code, call.pageURL, "alice@example.com"} {
		if strings.Contains(out, secret) {
			t.Errorf("log must not contain code/page/recipient: %q", out)
		}
	}
}

func TestResetPasswordSuccess(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	uid := seedUser(f, "alice@example.com")
	auth := newTestAuthService(f)
	if _, err := auth.Login(ctx, "alice@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(ctx, "alice@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	seedResetToken(f, uid, "valid-reset-token", time.Now().Add(15*time.Minute), false)

	if err := s.ResetPassword(ctx, "valid-reset-token", "newpassword456"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if f.db.users[uid].PasswordHash != "encoded:newpassword456" {
		t.Error("new password must be stored as a fresh hash")
	}
	for _, rt := range f.db.resets {
		if rt.ConsumedAt == nil {
			t.Error("token must be consumed after a successful reset")
		}
	}
	for _, rs := range f.db.sessions {
		if rs.UserID == uid && rs.RevokedAt == nil {
			t.Error("all refresh sessions must be revoked on reset")
		}
	}
	if _, err := auth.Login(ctx, "alice@example.com", "newpassword456"); err != nil {
		t.Errorf("login with the new password must succeed: %v", err)
	}
}

func TestResetPasswordUnknownToken(t *testing.T) {
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	seedUser(f, "alice@example.com")
	if err := s.ResetPassword(context.Background(), "no-such-token", "newpassword456"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token must be unauthorized, got %v", err)
	}
}

func TestResetPasswordExpiredToken(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	uid := seedUser(f, "alice@example.com")
	seedResetToken(f, uid, "expired-token", time.Now().Add(-time.Minute), false)

	if err := s.ResetPassword(ctx, "expired-token", "newpassword456"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired token must be unauthorized, got %v", err)
	}
	if f.db.users[uid].PasswordHash != "encoded:password123" {
		t.Error("expired token must not change the password")
	}
	for _, rt := range f.db.resets {
		if rt.ConsumedAt != nil {
			t.Error("expired token must not be marked consumed")
		}
	}
}

func TestResetPasswordReusedToken(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	uid := seedUser(f, "alice@example.com")
	seedResetToken(f, uid, "single-use-token", time.Now().Add(15*time.Minute), false)

	if err := s.ResetPassword(ctx, "single-use-token", "newpassword456"); err != nil {
		t.Fatalf("first use must succeed: %v", err)
	}
	if err := s.ResetPassword(ctx, "single-use-token", "anotherpassword789"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("reused token must be unauthorized, got %v", err)
	}
	if f.db.users[uid].PasswordHash != "encoded:newpassword456" {
		t.Error("reused token must not change the password again")
	}
}

func TestResetPasswordWeakPassword(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	uid := seedUser(f, "alice@example.com")
	seedResetToken(f, uid, "weak-pw-token", time.Now().Add(15*time.Minute), false)

	if err := s.ResetPassword(ctx, "weak-pw-token", "short"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("weak password must be invalid, got %v", err)
	}
	if f.db.users[uid].PasswordHash != "encoded:password123" {
		t.Error("weak password must not change the stored hash")
	}
	for _, rt := range f.db.resets {
		if rt.ConsumedAt != nil {
			t.Error("token must remain unconsumed when the new password is invalid")
		}
	}
}

func TestResetPasswordRollbackOnRevocationFailure(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	s := newTestResetService(f, newRecordingMailer(), nil)
	uid := seedUser(f, "alice@example.com")
	seedResetToken(f, uid, "rollback-token", time.Now().Add(15*time.Minute), false)
	f.revokeErr = errors.New("injected revocation failure")

	if err := s.ResetPassword(ctx, "rollback-token", "newpassword456"); err == nil {
		t.Fatal("injected failure must propagate")
	}
	if f.db.users[uid].PasswordHash != "encoded:password123" {
		t.Error("failed reset must roll back the password change")
	}
	for _, rt := range f.db.resets {
		if rt.ConsumedAt != nil {
			t.Error("failed reset must roll back token consumption")
		}
	}
}

func TestResetTokenHashOnlyStorage(t *testing.T) {
	f := newFakeStore()
	uid := seedUser(f, "alice@example.com")
	raw := "a-very-secret-reset-token-value"
	id := seedResetToken(f, uid, raw, time.Now().Add(15*time.Minute), false)
	rt := f.db.resets[id]
	if len(rt.TokenHash) != domain.ResetTokenHashLen {
		t.Errorf("hash must be %d bytes, got %d", domain.ResetTokenHashLen, len(rt.TokenHash))
	}
	sum := sha256.Sum256([]byte(raw))
	if !bytes.Equal(rt.TokenHash, sum[:]) {
		t.Error("only the SHA-256 digest may be stored")
	}
	if strings.Contains(string(rt.TokenHash), raw) {
		t.Error("raw token must never appear in storage")
	}
}
