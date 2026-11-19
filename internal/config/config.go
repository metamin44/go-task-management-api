// Package config loads and validates application configuration.
//
// Values come from environment variables, optionally seeded from a local
// .env file (development only). Environment variables always take precedence
// over .env values. Configuration is validated once at startup; invalid
// configuration fails fast with errors that mention variable names and safe
// explanations only — never supplied values or secrets.
//
// SECURITY: the Config object and DBConfig.DSN() contain secrets. They must
// never be logged, printed, or attached to error messages. Config is consumed
// only by the composition root (cmd/api); handlers and services never receive
// it. No String() or logging helpers are provided for that reason.
package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// AppConfig holds application-level settings.
type AppConfig struct {
	Env              string        // APP_ENV: development|staging|production
	HTTPPort         int           // HTTP_PORT: 1-65535
	HTTPReadTimeout  time.Duration // HTTP_READ_TIMEOUT: > 0
	HTTPWriteTimeout time.Duration // HTTP_WRITE_TIMEOUT: > 0
	HTTPIdleTimeout  time.Duration // HTTP_IDLE_TIMEOUT: > 0
}

// DBConfig holds PostgreSQL connection and pool settings.
type DBConfig struct {
	Host            string        // DB_HOST
	Port            int           // DB_PORT: 1-65535
	Name            string        // DB_NAME: required
	User            string        // DB_USER: required
	Password        string        // DB_PASSWORD: required secret — never log or print
	SSLMode         string        // DB_SSLMODE: required, no default
	MaxConns        int32         // DB_MAX_CONNS: >= 1
	MinConns        int32         // DB_MIN_CONNS: >= 0 and <= MaxConns
	MaxConnLifetime time.Duration // DB_MAX_CONN_LIFETIME: > 0
}

// AuthConfig holds token signing and lifetime settings.
type AuthConfig struct {
	AccessSecret      string        // AUTH_ACCESS_SECRET: required, >= 32 bytes — never log or print
	AccessTTL         time.Duration // AUTH_ACCESS_TTL: > 0, default 15m
	RefreshTTL        time.Duration // AUTH_REFRESH_TTL: > 0, default 720h (30 days)
	RateLimitInterval time.Duration // AUTH_RATE_LIMIT_INTERVAL: > 0, default 1s (per-IP auth rate limit)
	CleanupInterval   time.Duration // AUTH_CLEANUP_INTERVAL: > 0, default 1h (refresh-session cleanup tick)
}

// Config is the validated application configuration. It is immutable after
// Load returns and must never be logged or printed (it contains secrets).
type Config struct {
	App  AppConfig
	DB   DBConfig
	Auth AuthConfig
}

// DSN builds the PostgreSQL connection URL for pgxpool. Userinfo, host, path
// and query components are escaped via net/url.
//
// SECURITY: the returned URL embeds the database password. Pass it only to
// pgxpool.New / pgxpool.ParseConfig. Never log, print, or include it in error
// messages, traces, or panics.
func (c DBConfig) DSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   c.Name,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// Load reads configuration from the real process environment, seeded from an
// optional .env file in the current working directory. A missing .env is
// allowed; a malformed or unreadable .env is an error. Environment variables
// take precedence over .env values. On validation failure the returned error
// lists every offending variable name with a safe explanation, never values.
func Load() (*Config, error) {
	return load(os.LookupEnv, ".env")
}

// load is the isolated core. It accepts an environment lookup function and a
// dotenv path so tests can run without touching the process environment or
// working directory. The dotenv file is parsed with godotenv.Read, which does
// not modify the process environment; precedence is applied here explicitly.
func load(lookup func(string) (string, bool), dotenvPath string) (*Config, error) {
	var fileVals map[string]string
	if dotenvPath != "" {
		var err error
		fileVals, err = godotenv.Read(dotenvPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			// Do not wrap the underlying error: parser messages may echo
			// file content, which can contain secrets.
			return nil, fmt.Errorf("config: dotenv file %q is malformed or unreadable", dotenvPath)
		}
	}

	get := func(key string) (string, bool) {
		if v, ok := lookup(key); ok {
			return v, true // environment wins, even if explicitly empty
		}
		v, ok := fileVals[key]
		return v, ok
	}

	l := &loader{get: get}
	cfg := &Config{}

	cfg.App.Env = l.enum("APP_ENV", "development", false, "development", "staging", "production")
	port, _ := l.integer("HTTP_PORT", 8080, 1, 65535)
	cfg.App.HTTPPort = int(port)
	cfg.App.HTTPReadTimeout, _ = l.duration("HTTP_READ_TIMEOUT", 5*time.Second)
	cfg.App.HTTPWriteTimeout, _ = l.duration("HTTP_WRITE_TIMEOUT", 10*time.Second)
	cfg.App.HTTPIdleTimeout, _ = l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second)

	cfg.DB.Host = l.nonBlank("DB_HOST", "127.0.0.1", false)
	dbPort, _ := l.integer("DB_PORT", 5432, 1, 65535)
	cfg.DB.Port = int(dbPort)
	cfg.DB.Name = l.nonBlank("DB_NAME", "", true)
	cfg.DB.User = l.nonBlank("DB_USER", "", true)
	cfg.DB.Password = l.str("DB_PASSWORD", "", true)
	cfg.DB.SSLMode = l.enum("DB_SSLMODE", "", true,
		"disable", "allow", "prefer", "require", "verify-ca", "verify-full")

	maxConns, maxOK := l.integer("DB_MAX_CONNS", 10, 1, math.MaxInt32)
	minConns, minOK := l.integer("DB_MIN_CONNS", 0, 0, math.MaxInt32)
	cfg.DB.MaxConns = int32(maxConns)
	cfg.DB.MinConns = int32(minConns)
	if maxOK && minOK && minConns > maxConns {
		l.fail("DB_MIN_CONNS", "must be less than or equal to DB_MAX_CONNS")
	}
	cfg.DB.MaxConnLifetime, _ = l.duration("DB_MAX_CONN_LIFETIME", 30*time.Minute)

	// Authentication. The secret is preserved exactly as supplied (never
	// trimmed) and only its byte length is validated — the value must never
	// appear in errors or logs. 32 bytes matches service.MinAccessSecretLen.
	cfg.Auth.AccessSecret = l.str("AUTH_ACCESS_SECRET", "", true)
	if cfg.Auth.AccessSecret != "" && len(cfg.Auth.AccessSecret) < 32 {
		l.fail("AUTH_ACCESS_SECRET", "must be at least 32 bytes")
	}
	cfg.Auth.AccessTTL, _ = l.duration("AUTH_ACCESS_TTL", 15*time.Minute)
	cfg.Auth.RefreshTTL, _ = l.duration("AUTH_REFRESH_TTL", 720*time.Hour)
	cfg.Auth.RateLimitInterval, _ = l.duration("AUTH_RATE_LIMIT_INTERVAL", time.Second)
	cfg.Auth.CleanupInterval, _ = l.duration("AUTH_CLEANUP_INTERVAL", time.Hour)

	if len(l.errs) > 0 {
		return nil, errors.Join(l.errs...)
	}
	return cfg, nil
}

type loader struct {
	get  func(string) (string, bool)
	errs []error
}

// fail records an error containing only the variable name and a safe
// explanation. Supplied values must never appear in these messages.
func (l *loader) fail(key, reason string) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, reason))
}

func (l *loader) str(key, def string, required bool) string {
	v, ok := l.get(key)
	if !ok {
		if required {
			l.fail(key, "required, must be set to a non-empty value")
		}
		return def
	}
	if v == "" {
		l.fail(key, "must be a non-empty value")
		return def
	}
	return v
}

// nonBlank is like str but additionally rejects whitespace-only values
// (Unicode-aware). Used for DB_HOST, DB_NAME and DB_USER. It is deliberately
// NOT applied to DB_PASSWORD: the password is preserved exactly as supplied,
// never trimmed or otherwise modified. Accepted values are returned unmodified.
func (l *loader) nonBlank(key, def string, required bool) string {
	v := l.str(key, def, required)
	if v != "" && strings.TrimSpace(v) == "" {
		l.fail(key, "must contain at least one non-whitespace character")
	}
	return v
}

func (l *loader) enum(key, def string, required bool, allowed ...string) string {
	v := l.str(key, def, required)
	if v == "" {
		return v // missing required value already reported
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail(key, "must be one of "+strings.Join(allowed, "|"))
	return ""
}

func (l *loader) integer(key string, def, min, max int64) (int64, bool) {
	v, ok := l.get(key)
	if !ok {
		return def, true
	}
	if v == "" {
		l.fail(key, "must be a non-empty integer")
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.fail(key, "must be an integer")
		return 0, false
	}
	if n < min || n > max {
		l.fail(key, fmt.Sprintf("must be between %d and %d", min, max))
		return 0, false
	}
	return n, true
}

func (l *loader) duration(key string, def time.Duration) (time.Duration, bool) {
	v, ok := l.get(key)
	if !ok {
		return def, true
	}
	if v == "" {
		l.fail(key, "must be a non-empty duration")
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, "must be a valid duration, e.g. 5s or 30m")
		return 0, false
	}
	if d <= 0 {
		l.fail(key, "must be greater than zero")
		return 0, false
	}
	return d, true
}
