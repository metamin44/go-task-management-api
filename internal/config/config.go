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
	ResetTTL          time.Duration // AUTH_RESET_TTL: > 0, default 15m (password-reset token lifetime)
	ResetBaseURL      string        // PASSWORD_RESET_BASE_URL: required absolute http(s) URL for the reset link
}

// SMTPConfig holds outbound email settings for password-reset messages.
type SMTPConfig struct {
	Host       string // SMTP_HOST: required
	Port       int    // SMTP_PORT: 1-65535, default 587
	Username   string // SMTP_USERNAME: optional; authentication is enabled when set
	Password   string // SMTP_PASSWORD: required when SMTP_USERNAME is set — never log or print
	From       string // SMTP_FROM: required sender address
	RequireTLS bool   // SMTP_REQUIRE_TLS: default true; fail closed without STARTTLS (localhost dev exception when false)
}

// Addr returns the host:port dial address for the SMTP server.
func (c SMTPConfig) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// Config is the validated application configuration. It is immutable after
// Load returns and must never be logged or printed (it contains secrets).
type Config struct {
	App  AppConfig
	DB   DBConfig
	Auth AuthConfig
	SMTP SMTPConfig
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

// Load reads the full application configuration from the real process
// environment, seeded from an optional .env file in the current working
// directory. A missing .env is allowed; a malformed or unreadable .env is an
// error. Environment variables take precedence over .env values. On validation
// failure the returned error lists every offending variable name with a safe
// explanation, never values.
func Load() (*Config, error) {
	return load(os.LookupEnv, ".env")
}

// LoadDB reads ONLY the database configuration (environment over optional
// .env). It is used by cmd/migrate, which needs database settings but must not
// require application secrets such as AUTH_ACCESS_SECRET or SMTP credentials.
func LoadDB() (DBConfig, error) {
	return loadDB(os.LookupEnv, ".env")
}

// newLoader reads the optional dotenv file and builds a loader applying
// env-over-file precedence. godotenv.Read does not modify the process
// environment. A malformed/unreadable file is a secret-safe error (parser
// messages may echo file content, so the underlying error is never wrapped).
func newLoader(lookup func(string) (string, bool), dotenvPath string) (*loader, error) {
	var fileVals map[string]string
	if dotenvPath != "" {
		var err error
		fileVals, err = godotenv.Read(dotenvPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
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
	return &loader{get: get}, nil
}

// loadDBFields loads and validates the database settings onto l. Shared by the
// full loader and the DB-only loader so the rules stay identical.
func loadDBFields(l *loader) DBConfig {
	var db DBConfig
	db.Host = l.nonBlank("DB_HOST", "127.0.0.1", false)
	dbPort, _ := l.integer("DB_PORT", 5432, 1, 65535)
	db.Port = int(dbPort)
	db.Name = l.nonBlank("DB_NAME", "", true)
	db.User = l.nonBlank("DB_USER", "", true)
	db.Password = l.str("DB_PASSWORD", "", true)
	db.SSLMode = l.enum("DB_SSLMODE", "", true,
		"disable", "allow", "prefer", "require", "verify-ca", "verify-full")

	maxConns, maxOK := l.integer("DB_MAX_CONNS", 10, 1, math.MaxInt32)
	minConns, minOK := l.integer("DB_MIN_CONNS", 0, 0, math.MaxInt32)
	db.MaxConns = int32(maxConns)
	db.MinConns = int32(minConns)
	if maxOK && minOK && minConns > maxConns {
		l.fail("DB_MIN_CONNS", "must be less than or equal to DB_MAX_CONNS")
	}
	db.MaxConnLifetime, _ = l.duration("DB_MAX_CONN_LIFETIME", 30*time.Minute)
	return db
}

// loadDB is the isolated DB-only core (injectable lookup/dotenv for tests).
func loadDB(lookup func(string) (string, bool), dotenvPath string) (DBConfig, error) {
	l, err := newLoader(lookup, dotenvPath)
	if err != nil {
		return DBConfig{}, err
	}
	db := loadDBFields(l)
	if len(l.errs) > 0 {
		return DBConfig{}, errors.Join(l.errs...)
	}
	return db, nil
}

// load is the isolated full-configuration core. It accepts an environment
// lookup function and a dotenv path so tests can run without touching the
// process environment or working directory.
func load(lookup func(string) (string, bool), dotenvPath string) (*Config, error) {
	l, err := newLoader(lookup, dotenvPath)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}

	cfg.App.Env = l.enum("APP_ENV", "development", false, "development", "staging", "production")
	port, _ := l.integer("HTTP_PORT", 8080, 1, 65535)
	cfg.App.HTTPPort = int(port)
	cfg.App.HTTPReadTimeout, _ = l.duration("HTTP_READ_TIMEOUT", 5*time.Second)
	cfg.App.HTTPWriteTimeout, _ = l.duration("HTTP_WRITE_TIMEOUT", 10*time.Second)
	cfg.App.HTTPIdleTimeout, _ = l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second)

	cfg.DB = loadDBFields(l)

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
	cfg.Auth.ResetTTL, _ = l.duration("AUTH_RESET_TTL", 15*time.Minute)
	cfg.Auth.ResetBaseURL = l.str("PASSWORD_RESET_BASE_URL", "", true)
	if cfg.Auth.ResetBaseURL != "" {
		u, err := url.Parse(cfg.Auth.ResetBaseURL)
		// S-06: the reset page URL must be a clean absolute http(s) URL — no
		// query, fragment, or opaque part (the emailed code is never URL-borne).
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.User != nil {
			l.fail("PASSWORD_RESET_BASE_URL", "must be an absolute http or https URL without query, fragment, or userinfo")
		}
	}

	// Outbound SMTP for password-reset email. Host and sender are required;
	// username/password are an optional pair (authentication only when the
	// username is set). The password is a secret: name-only errors, never logged.
	cfg.SMTP.Host = l.nonBlank("SMTP_HOST", "", true)
	smtpPort, _ := l.integer("SMTP_PORT", 587, 1, 65535)
	cfg.SMTP.Port = int(smtpPort)
	cfg.SMTP.From = l.nonBlank("SMTP_FROM", "", true)
	if cfg.SMTP.From != "" && !strings.Contains(cfg.SMTP.From, "@") {
		l.fail("SMTP_FROM", "must be a valid sender email address")
	}
	cfg.SMTP.Username = l.str("SMTP_USERNAME", "", false)
	cfg.SMTP.Password = l.str("SMTP_PASSWORD", "", false)
	if cfg.SMTP.Username != "" && cfg.SMTP.Password == "" {
		l.fail("SMTP_PASSWORD", "required when SMTP_USERNAME is set")
	}
	if cfg.SMTP.Username == "" && cfg.SMTP.Password != "" {
		l.fail("SMTP_USERNAME", "required when SMTP_PASSWORD is set")
	}
	// S-01: secure by default — require TLS. Set false only for a local dev
	// SMTP server (e.g. MailHog); remote servers always fail closed without TLS.
	cfg.SMTP.RequireTLS = l.boolean("SMTP_REQUIRE_TLS", true)

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

// boolean parses a true/false value (strconv.ParseBool accepts 1/0/t/f/true/
// false etc.). Missing uses the default; an explicit empty or unparseable value
// is a name-only error.
func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.get(key)
	if !ok {
		return def
	}
	if v == "" {
		l.fail(key, "must be a non-empty boolean")
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be a boolean (true or false)")
		return def
	}
	return b
}
