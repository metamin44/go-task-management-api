package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// All tests exercise the isolated load() core with injected lookup functions
// and temp-dir fixtures. The public Load() is deliberately NOT called here:
// it would read the real process environment and the real .env file.
// Only dummy credentials are used, never real secrets.

const dummySecret = "dummy-secret-for-tests-only"

// 32-byte dummy signing secret (tests only, never a real secret).
const dummyAuthSecret = "0123456789abcdef0123456789abcdef"

func validEnv() map[string]string {
	return map[string]string{
		"DB_NAME":            "task_management",
		"DB_USER":            "app_user",
		"DB_PASSWORD":        dummySecret,
		"DB_SSLMODE":         "disable",
		"AUTH_ACCESS_SECRET": dummyAuthSecret,
	}
}

func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(mapLookup(validEnv()), "")
	if err != nil {
		t.Fatalf("load with minimal required env: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"APP_ENV", cfg.App.Env, "development"},
		{"HTTP_PORT", cfg.App.HTTPPort, 8080},
		{"HTTP_READ_TIMEOUT", cfg.App.HTTPReadTimeout, 5 * time.Second},
		{"HTTP_WRITE_TIMEOUT", cfg.App.HTTPWriteTimeout, 10 * time.Second},
		{"HTTP_IDLE_TIMEOUT", cfg.App.HTTPIdleTimeout, 60 * time.Second},
		{"DB_HOST", cfg.DB.Host, "127.0.0.1"},
		{"DB_PORT", cfg.DB.Port, 5432},
		{"DB_NAME", cfg.DB.Name, "task_management"},
		{"DB_USER", cfg.DB.User, "app_user"},
		{"DB_SSLMODE", cfg.DB.SSLMode, "disable"},
		{"DB_MAX_CONNS", cfg.DB.MaxConns, int32(10)},
		{"DB_MIN_CONNS", cfg.DB.MinConns, int32(0)},
		{"DB_MAX_CONN_LIFETIME", cfg.DB.MaxConnLifetime, 30 * time.Minute},
		{"AUTH_ACCESS_TTL", cfg.Auth.AccessTTL, 15 * time.Minute},
		{"AUTH_REFRESH_TTL", cfg.Auth.RefreshTTL, 720 * time.Hour},
		{"AUTH_RATE_LIMIT_INTERVAL", cfg.Auth.RateLimitInterval, time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// Assert the secret separately: never print its value, even on failure.
	if cfg.DB.Password != dummySecret {
		t.Error("DB_PASSWORD not loaded correctly (value withheld)")
	}
	if cfg.Auth.AccessSecret != dummyAuthSecret {
		t.Error("AUTH_ACCESS_SECRET not loaded correctly (value withheld)")
	}
}

func TestLoadOverrides(t *testing.T) {
	env := validEnv()
	env["APP_ENV"] = "production"
	env["HTTP_PORT"] = "9090"
	env["HTTP_READ_TIMEOUT"] = "15s"
	env["HTTP_WRITE_TIMEOUT"] = "20s"
	env["HTTP_IDLE_TIMEOUT"] = "120s"
	env["DB_HOST"] = "db.internal"
	env["DB_PORT"] = "5433"
	env["DB_SSLMODE"] = "verify-full"
	env["DB_MAX_CONNS"] = "25"
	env["DB_MIN_CONNS"] = "5"
	env["DB_MAX_CONN_LIFETIME"] = "1h"
	env["AUTH_ACCESS_TTL"] = "5m"
	env["AUTH_REFRESH_TTL"] = "168h"
	env["AUTH_RATE_LIMIT_INTERVAL"] = "2s"

	cfg, err := load(mapLookup(env), "")
	if err != nil {
		t.Fatalf("load with overrides: %v", err)
	}
	if cfg.App.Env != "production" || cfg.App.HTTPPort != 9090 ||
		cfg.App.HTTPReadTimeout != 15*time.Second || cfg.App.HTTPWriteTimeout != 20*time.Second ||
		cfg.App.HTTPIdleTimeout != 120*time.Second {
		t.Errorf("app overrides not applied: %+v", cfg.App)
	}
	if cfg.DB.Host != "db.internal" {
		t.Errorf("DB_HOST = %q, want %q", cfg.DB.Host, "db.internal")
	}
	if cfg.DB.Port != 5433 {
		t.Errorf("DB_PORT = %d, want 5433", cfg.DB.Port)
	}
	if cfg.DB.SSLMode != "verify-full" {
		t.Errorf("DB_SSLMODE = %q, want %q", cfg.DB.SSLMode, "verify-full")
	}
	if cfg.DB.MaxConns != 25 {
		t.Errorf("DB_MAX_CONNS = %d, want 25", cfg.DB.MaxConns)
	}
	if cfg.DB.MinConns != 5 {
		t.Errorf("DB_MIN_CONNS = %d, want 5", cfg.DB.MinConns)
	}
	if cfg.DB.MaxConnLifetime != time.Hour {
		t.Errorf("DB_MAX_CONN_LIFETIME = %v, want 1h", cfg.DB.MaxConnLifetime)
	}
	if cfg.Auth.AccessTTL != 5*time.Minute {
		t.Errorf("AUTH_ACCESS_TTL = %v, want 5m", cfg.Auth.AccessTTL)
	}
	if cfg.Auth.RefreshTTL != 168*time.Hour {
		t.Errorf("AUTH_REFRESH_TTL = %v, want 168h", cfg.Auth.RefreshTTL)
	}
	if cfg.Auth.RateLimitInterval != 2*time.Second {
		t.Errorf("AUTH_RATE_LIMIT_INTERVAL = %v, want 2s", cfg.Auth.RateLimitInterval)
	}
}

func TestLoadRequired(t *testing.T) {
	for _, key := range []string{"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_SSLMODE", "AUTH_ACCESS_SECRET"} {
		t.Run("missing_"+key, func(t *testing.T) {
			env := validEnv()
			delete(env, key)
			_, err := load(mapLookup(env), "")
			if err == nil {
				t.Fatalf("expected error for missing %s", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error must name %s", key)
			}
			if strings.Contains(err.Error(), dummySecret) {
				t.Errorf("error must never contain the supplied secret")
			}
		})
		t.Run("empty_"+key, func(t *testing.T) {
			env := validEnv()
			env[key] = ""
			_, err := load(mapLookup(env), "")
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected error naming %s for empty value, got %v", key, err)
			}
		})
	}
}

func TestLoadInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"app_env_enum", "APP_ENV", "banana"},
		{"port_not_int", "HTTP_PORT", "abc"},
		{"port_zero", "HTTP_PORT", "0"},
		{"port_negative", "HTTP_PORT", "-1"},
		{"port_too_big", "HTTP_PORT", "65536"},
		{"port_empty", "HTTP_PORT", ""},
		{"db_port_not_int", "DB_PORT", "abc"},
		{"db_port_zero", "DB_PORT", "0"},
		{"read_timeout_invalid", "HTTP_READ_TIMEOUT", "abc"},
		{"read_timeout_zero", "HTTP_READ_TIMEOUT", "0s"},
		{"read_timeout_negative", "HTTP_READ_TIMEOUT", "-1s"},
		{"read_timeout_empty", "HTTP_READ_TIMEOUT", ""},
		{"write_timeout_invalid", "HTTP_WRITE_TIMEOUT", "abc"},
		{"idle_timeout_invalid", "HTTP_IDLE_TIMEOUT", "abc"},
		{"max_conns_zero", "DB_MAX_CONNS", "0"},
		{"max_conns_not_int", "DB_MAX_CONNS", "abc"},
		{"min_conns_negative", "DB_MIN_CONNS", "-1"},
		{"min_conns_not_int", "DB_MIN_CONNS", "abc"},
		{"lifetime_invalid", "DB_MAX_CONN_LIFETIME", "abc"},
		{"lifetime_zero", "DB_MAX_CONN_LIFETIME", "0s"},
		{"sslmode_enum", "DB_SSLMODE", "banana"},
		{"host_empty", "DB_HOST", ""},
		{"name_empty", "DB_NAME", ""},
		{"access_secret_short", "AUTH_ACCESS_SECRET", "too-short-secret"},
		{"access_secret_31_bytes", "AUTH_ACCESS_SECRET", "0123456789abcdef0123456789abcde"},
		{"access_ttl_invalid", "AUTH_ACCESS_TTL", "abc"},
		{"access_ttl_zero", "AUTH_ACCESS_TTL", "0s"},
		{"refresh_ttl_invalid", "AUTH_REFRESH_TTL", "abc"},
		{"refresh_ttl_negative", "AUTH_REFRESH_TTL", "-1h"},
		{"rate_limit_invalid", "AUTH_RATE_LIMIT_INTERVAL", "abc"},
		{"rate_limit_zero", "AUTH_RATE_LIMIT_INTERVAL", "0s"},
		{"rate_limit_negative", "AUTH_RATE_LIMIT_INTERVAL", "-1s"},
		{"rate_limit_empty", "AUTH_RATE_LIMIT_INTERVAL", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := load(mapLookup(env), "")
			if err == nil {
				t.Fatalf("expected error for %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error must name %s", tc.key)
			}
			if strings.Contains(err.Error(), dummySecret) {
				t.Errorf("error must never contain the supplied secret")
			}
		})
	}
}

func TestErrorMessagesNeverContainSuppliedValues(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		value     string
		wantInErr string
	}{
		{"enum_value", "APP_ENV", "s3cr3t-enum-value", "APP_ENV"},
		{"int_value", "HTTP_PORT", "999999", "HTTP_PORT"},
		{"duration_value", "HTTP_READ_TIMEOUT", "s3cr3t-duration", "HTTP_READ_TIMEOUT"},
		{"sslmode_value", "DB_SSLMODE", "s3cr3t-sslmode", "DB_SSLMODE"},
		{"access_secret_value", "AUTH_ACCESS_SECRET", "s3cr3t-short-secret", "AUTH_ACCESS_SECRET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := load(mapLookup(env), "")
			if err == nil {
				t.Fatalf("expected error for %s", tc.key)
			}
			msg := err.Error()
			if strings.Contains(msg, tc.value) {
				t.Errorf("error message must not contain the supplied value")
			}
			if strings.Contains(msg, dummySecret) {
				t.Errorf("error message must not contain the secret")
			}
			if !strings.Contains(msg, tc.wantInErr) {
				t.Errorf("error message must name %s", tc.wantInErr)
			}
		})
	}
	t.Run("all_errors_accumulated", func(t *testing.T) {
		env := map[string]string{"HTTP_PORT": "999999", "APP_ENV": "s3cr3t-enum-value"}
		_, err := load(mapLookup(env), "")
		if err == nil {
			t.Fatal("expected joined errors")
		}
		msg := err.Error()
		for _, key := range []string{"HTTP_PORT", "APP_ENV", "DB_NAME", "DB_USER", "DB_PASSWORD", "DB_SSLMODE", "AUTH_ACCESS_SECRET"} {
			if !strings.Contains(msg, key) {
				t.Errorf("joined error must name %s", key)
			}
		}
		if strings.Contains(msg, "999999") || strings.Contains(msg, "s3cr3t-enum-value") {
			t.Error("joined error must not contain supplied values")
		}
	})
}

func TestPoolCrossFieldValidation(t *testing.T) {
	cases := []struct {
		name    string
		min     string
		max     string
		wantErr bool
	}{
		{"min_above_max", "20", "10", true},
		{"min_equals_max", "10", "10", false},
		{"min_below_max", "2", "10", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["DB_MIN_CONNS"] = tc.min
			env["DB_MAX_CONNS"] = tc.max
			_, err := load(mapLookup(env), "")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") ||
					!strings.Contains(err.Error(), "DB_MAX_CONNS") {
					t.Fatalf("expected cross-field error naming both keys, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	t.Run("no_cross_error_when_max_invalid", func(t *testing.T) {
		env := validEnv()
		env["DB_MAX_CONNS"] = "abc"
		env["DB_MIN_CONNS"] = "20"
		_, err := load(mapLookup(env), "")
		if err == nil || !strings.Contains(err.Error(), "DB_MAX_CONNS") {
			t.Fatalf("expected DB_MAX_CONNS parse error, got %v", err)
		}
		if strings.Contains(err.Error(), "less than or equal") {
			t.Error("cross-field check must be skipped when a value failed to parse")
		}
	})
}

func TestDotenvMissingIsAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.env")
	cfg, err := load(mapLookup(validEnv()), path)
	if err != nil {
		t.Fatalf("missing dotenv must be allowed: %v", err)
	}
	if cfg.DB.Name != "task_management" {
		t.Errorf("DB_NAME = %q, want %q", cfg.DB.Name, "task_management")
	}
}

func TestDotenvMalformedFails(t *testing.T) {
	content := "DB_PASSWORD=" + dummySecret + "\nthis line has no equals sign\n"
	path := writeFixture(t, content)
	_, err := load(mapLookup(validEnv()), path)
	if err == nil {
		t.Fatal("malformed dotenv must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "malformed") {
		t.Errorf("error must explain the dotenv problem safely, got: %s", msg)
	}
	if strings.Contains(msg, dummySecret) || strings.Contains(msg, "this line") {
		t.Error("error must never echo dotenv file content")
	}
}

func TestDotenvValuesUsedWhenEnvAbsent(t *testing.T) {
	path := writeFixture(t, "DB_NAME=filedb\nDB_USER=fileuser\nDB_PASSWORD=file-"+dummySecret+"\nDB_SSLMODE=disable\nDB_HOST=10.10.10.10\nHTTP_PORT=7070\nAUTH_ACCESS_SECRET="+dummyAuthSecret+"\n")
	cfg, err := load(mapLookup(map[string]string{}), path)
	if err != nil {
		t.Fatalf("load from dotenv only: %v", err)
	}
	if cfg.DB.Name != "filedb" {
		t.Errorf("DB_NAME = %q, want %q", cfg.DB.Name, "filedb")
	}
	if cfg.DB.User != "fileuser" {
		t.Errorf("DB_USER = %q, want %q", cfg.DB.User, "fileuser")
	}
	if cfg.DB.Host != "10.10.10.10" {
		t.Errorf("DB_HOST = %q, want %q", cfg.DB.Host, "10.10.10.10")
	}
	if cfg.App.HTTPPort != 7070 {
		t.Errorf("HTTP_PORT = %d, want 7070", cfg.App.HTTPPort)
	}
	// Assert the secret separately: never print its value, even on failure.
	if cfg.DB.Password != "file-"+dummySecret {
		t.Error("DB_PASSWORD from dotenv not applied (value withheld)")
	}
	if cfg.Auth.AccessSecret != dummyAuthSecret {
		t.Error("AUTH_ACCESS_SECRET from dotenv not applied (value withheld)")
	}
}

func TestEnvPrecedenceOverDotenv(t *testing.T) {
	path := writeFixture(t, "DB_NAME=filedb\nDB_USER=fileuser\nDB_PASSWORD=file-"+dummySecret+"\nDB_SSLMODE=disable\nDB_HOST=10.10.10.10\nHTTP_PORT=7070\nAUTH_ACCESS_SECRET="+dummyAuthSecret+"\n")
	env := map[string]string{
		"DB_NAME":     "envdb",
		"DB_PASSWORD": "env-" + dummySecret,
	}
	// Snapshot process env to prove load() never mutates it.
	keys := []string{"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_HOST", "HTTP_PORT", "DB_SSLMODE"}
	before := make(map[string]string, len(keys))
	for _, k := range keys {
		before[k] = os.Getenv(k)
	}

	cfg, err := load(mapLookup(env), path)
	if err != nil {
		t.Fatalf("load with env over dotenv: %v", err)
	}
	if cfg.DB.Name != "envdb" || cfg.DB.Password != "env-"+dummySecret {
		t.Error("environment must take precedence over dotenv")
	}
	if cfg.DB.User != "fileuser" || cfg.DB.Host != "10.10.10.10" || cfg.App.HTTPPort != 7070 {
		t.Error("dotenv must fill keys absent from the environment")
	}
	for _, k := range keys {
		if os.Getenv(k) != before[k] {
			t.Errorf("load() mutated process environment key %s", k)
		}
	}
}

func TestExplicitEmptyEnvOverridesDotenv(t *testing.T) {
	path := writeFixture(t, "DB_NAME=filedb\nDB_USER=fileuser\nDB_PASSWORD=file-"+dummySecret+"\nDB_SSLMODE=disable\n")
	env := map[string]string{"DB_NAME": ""} // explicitly empty wins over file, then fails validation
	_, err := load(mapLookup(env), path)
	if err == nil || !strings.Contains(err.Error(), "DB_NAME") {
		t.Fatalf("explicit empty env value must fail validation, got %v", err)
	}
}

func TestDSN(t *testing.T) {
	cases := []struct {
		name     string
		cfg      DBConfig
		wantHost string
	}{
		{
			name: "special_chars",
			cfg: DBConfig{
				Host: "127.0.0.1", Port: 5432, Name: "task_management",
				User: "app_user", Password: "p@ss:w/rd?#%", SSLMode: "disable",
			},
			wantHost: "127.0.0.1:5432",
		},
		{
			name: "ipv6_host",
			cfg: DBConfig{
				Host: "::1", Port: 5433, Name: "task_management",
				User: "app_user", Password: dummySecret, SSLMode: "verify-full",
			},
			wantHost: "[::1]:5433",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := tc.cfg.DSN()
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("DSN must be a valid URL: %v", err)
			}
			if u.Scheme != "postgres" {
				t.Errorf("scheme = %q, want postgres", u.Scheme)
			}
			if u.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", u.Host, tc.wantHost)
			}
			if u.Path != "/task_management" {
				t.Errorf("path = %q, want /task_management", u.Path)
			}
			if got := u.User.Username(); got != tc.cfg.User {
				t.Errorf("user = %q, want %q", got, tc.cfg.User)
			}
			if got, _ := u.User.Password(); got != tc.cfg.Password {
				t.Error("password must round-trip through URL escaping")
			}
			if got := u.Query().Get("sslmode"); got != tc.cfg.SSLMode {
				t.Errorf("sslmode = %q, want %q", got, tc.cfg.SSLMode)
			}
			if strings.Contains(dsn, "p@ss:w/rd?#%") {
				t.Error("special characters must be escaped in the DSN")
			}
		})
	}
}

func TestWhitespaceOnlyIdentifiers(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"host_spaces", "DB_HOST", "   "},
		{"host_tab", "DB_HOST", "\t"},
		{"host_newline", "DB_HOST", "\n"},
		{"host_mixed", "DB_HOST", " \t\n "},
		{"host_nbsp", "DB_HOST", " "},
		{"name_spaces", "DB_NAME", "   "},
		{"name_mixed", "DB_NAME", " \t\n "},
		{"user_spaces", "DB_USER", "   "},
		{"user_mixed", "DB_USER", " \t\n "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := load(mapLookup(env), "")
			if err == nil {
				t.Fatalf("expected error for whitespace-only %s", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error must name %s", tc.key)
			}
			if strings.Contains(err.Error(), dummySecret) {
				t.Error("error must never contain the supplied secret")
			}
		})
	}
}

func TestPasswordPreservedExactly(t *testing.T) {
	env := validEnv()
	env["DB_PASSWORD"] = "  " + dummySecret + "\t"
	cfg, err := load(mapLookup(env), "")
	if err != nil {
		t.Fatalf("password with surrounding whitespace must be accepted: %v", err)
	}
	if cfg.DB.Password != "  "+dummySecret+"\t" {
		t.Error("DB_PASSWORD must be preserved exactly as supplied, never trimmed")
	}
}

func TestIdentifiersNotTrimmed(t *testing.T) {
	env := validEnv()
	env["DB_NAME"] = " mydb "
	cfg, err := load(mapLookup(env), "")
	if err != nil {
		t.Fatalf("value with surrounding whitespace must be accepted: %v", err)
	}
	if cfg.DB.Name != " mydb " {
		t.Error("validation must not trim or modify accepted values")
	}
}
