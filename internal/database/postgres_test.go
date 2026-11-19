package database

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/go-task-management-api/internal/config"
)

// Static, secret-safe error messages that are part of NewPool's public contract.
const (
	msgParseConfig = "database: connection settings rejected by pgx (details withheld for security)"
	msgCreatePool  = "database: failed to create connection pool (details withheld for security)"
	msgPing        = "database: initial connection check failed (details withheld for security)"
)

// testSecret is a dummy value used only to prove it never leaks into errors.
const testSecret = "dummy-test-secret-not-real"

// freeLocalPort returns a 127.0.0.1 port with no listener: bind an ephemeral
// port, record it, and close it. Connecting to it yields an immediate
// connection refusal — no database, Docker, or external network involved.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local test port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release local test port: %v", err)
	}
	return port
}

// assertSafeError checks that an error message exposes none of the supplied
// connection details or the dummy secret. Failure messages never echo the
// sensitive values themselves.
func assertSafeError(t *testing.T, err error, cfg config.DBConfig) {
	t.Helper()
	msg := err.Error()
	checks := []struct {
		label string
		value string
	}{
		{"password", cfg.Password},
		{"user", cfg.User},
		{"database name", cfg.Name},
		{"host", cfg.Host},
	}
	for _, c := range checks {
		if c.value != "" && strings.Contains(msg, c.value) {
			t.Errorf("error message must not expose the %s", c.label)
		}
	}
}

func TestNewPoolInvalidConfig(t *testing.T) {
	cfg := config.DBConfig{
		Host:            "127.0.0.1",
		Port:            5432,
		Name:            "testdb",
		User:            "test-user",
		Password:        testSecret,
		SSLMode:         "banana", // rejected by pgxpool.ParseConfig; no network needed
		MaxConns:        2,
		MinConns:        0,
		MaxConnLifetime: time.Minute,
	}

	pool, err := NewPool(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error for invalid sslmode, got nil")
	}
	if pool != nil {
		t.Error("pool must be nil when configuration parsing fails")
	}
	if err.Error() != msgParseConfig {
		t.Errorf("error = %q, want the static safe message %q", err.Error(), msgParseConfig)
	}
	assertSafeError(t, err, cfg)
}

func TestNewPoolCanceledContext(t *testing.T) {
	cfg := config.DBConfig{
		Host:            "127.0.0.1",
		Port:            freeLocalPort(t),
		Name:            "testdb",
		User:            "test-user",
		Password:        testSecret,
		SSLMode:         "disable",
		MaxConns:        2,
		MinConns:        0,
		MaxConnLifetime: time.Minute,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the call

	pool, err := NewPool(ctx, cfg)
	if err == nil {
		t.Fatal("expected an error with a canceled context, got nil")
	}
	if pool != nil {
		t.Error("pool must be nil when the caller context is canceled")
	}
	// pgx may surface cancellation at pool creation or at the Ping check;
	// both outcomes are part of the public contract and equally safe.
	msg := err.Error()
	if msg != msgPing && msg != msgCreatePool {
		t.Errorf("error = %q, want one of the static safe messages", msg)
	}
	assertSafeError(t, err, cfg)
}

func TestNewPoolPingFailure(t *testing.T) {
	cfg := config.DBConfig{
		Host:            "127.0.0.1",
		Port:            freeLocalPort(t), // nothing listens here: immediate refusal
		Name:            "testdb",
		User:            "test-user",
		Password:        testSecret,
		SSLMode:         "disable",
		MaxConns:        2,
		MinConns:        0, // keep pool creation lazy; failure must come from Ping
		MaxConnLifetime: time.Minute,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err == nil {
		t.Fatal("expected an error when no server listens on the test port, got nil")
	}
	if pool != nil {
		t.Error("pool must be nil when the initial Ping fails (and must have been closed)")
	}
	if err.Error() != msgPing {
		t.Errorf("error = %q, want the static safe message %q", err.Error(), msgPing)
	}
	assertSafeError(t, err, cfg)
}
