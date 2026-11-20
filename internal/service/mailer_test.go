package service

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResetMessageFormat(t *testing.T) {
	pageURL := "https://app.example.com/reset"
	code := "abc123-single-use-code"
	msg := resetMessage("no-reply@example.com", "alice@example.com", pageURL, code)
	for _, want := range []string{
		"From: no-reply@example.com\r\n",
		"To: alice@example.com\r\n",
		"Subject: Reset your password\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"Reset page: " + pageURL + "\r\n",
		"Reset code: " + code + "\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q", want)
		}
	}
	// Headers and body separated by a blank CRLF line.
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Error("message must separate headers from body with a blank line")
	}
	// S-05: the code travels in the body, never appended to the page URL.
	if strings.Contains(pageURL, code) || strings.Contains(pageURL, "token") {
		t.Errorf("page URL must not carry the code: %q", pageURL)
	}
}

// TestSMTPMailerDialFailureSecretSafe points the mailer at a closed local port
// with a bounded context: the dial must fail and the returned error must be a
// static message that never echoes credentials, the page URL, or the code.
func TestSMTPMailerDialFailureSecretSafe(t *testing.T) {
	m := NewSMTPMailer("localhost", "127.0.0.1:1", "mailer-user", "super-smtp-secret", "no-reply@example.com", true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pageURL := "https://app.example.com/reset"
	code := "secret-token-value"
	err := m.SendPasswordReset(ctx, "alice@example.com", pageURL, code)
	if err == nil {
		t.Fatal("dialing a closed port must fail")
	}
	msg := err.Error()
	for _, secret := range []string{"super-smtp-secret", "mailer-user", code, pageURL, "alice@example.com"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error must not leak credentials, recipient, page, or code: %q", msg)
		}
	}
	if !strings.Contains(msg, "password reset email") {
		t.Errorf("error must be a static, contextual message: %q", msg)
	}
}

func TestIsLocalSMTPHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"smtp.example.com", false},
		{"mail.internal", false},
		{"10.0.0.5", false},
		{"", false},
	} {
		if got := isLocalSMTPHost(tc.host); got != tc.want {
			t.Errorf("isLocalSMTPHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestMustUseTLS is the S-01 decision matrix: TLS is required whenever the
// requirement is on OR the host is non-local. Plaintext is allowed only for a
// local host with the requirement explicitly disabled.
func TestMustUseTLS(t *testing.T) {
	for _, tc := range []struct {
		host       string
		requireTLS bool
		want       bool
	}{
		{"smtp.example.com", true, true},
		{"smtp.example.com", false, true}, // remote always fails closed
		{"localhost", true, true},         // requirement forces TLS even locally
		{"127.0.0.1", true, true},
		{"localhost", false, false}, // local dev exception
		{"127.0.0.1", false, false},
		{"::1", false, false},
	} {
		if got := mustUseTLS(tc.host, tc.requireTLS); got != tc.want {
			t.Errorf("mustUseTLS(%q, %v) = %v, want %v", tc.host, tc.requireTLS, got, tc.want)
		}
	}
}

// startFakeSMTP runs a minimal in-process SMTP server that never negotiates TLS.
// advertiseSTARTTLS controls whether the EHLO reply offers STARTTLS. It returns
// the dial address and a stop function. No real network or credentials are used.
func startFakeSMTP(t *testing.T, advertiseSTARTTLS bool) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		write := func(s string) { _, _ = w.WriteString(s); _ = w.Flush() }
		write("220 fake SMTP ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				if advertiseSTARTTLS {
					write("250-fake\r\n250-STARTTLS\r\n250 OK\r\n")
				} else {
					write("250 fake\r\n")
				}
			case cmd == "DATA":
				write("354 End data with <CR><LF>.<CR><LF>\r\n")
				for {
					dl, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(dl, "\r\n") == "." {
						break
					}
				}
				write("250 OK\r\n")
			case cmd == "QUIT":
				write("221 Bye\r\n")
				return
			default: // MAIL, RCPT, RSET, NOOP, ...
				write("250 OK\r\n")
			}
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close(); wg.Wait() }
}

// TestSMTPMailerFailsClosedWithoutSTARTTLS verifies S-01 end to end: a server
// that does not offer STARTTLS must be refused for a remote host (and for a
// local host when TLS is required), while a local dev server with the
// requirement disabled may deliver over plaintext.
func TestSMTPMailerFailsClosedWithoutSTARTTLS(t *testing.T) {
	pageURL := "https://app.example.com/reset"
	code := "single-use-code"

	t.Run("remote host fails closed", func(t *testing.T) {
		addr, stop := startFakeSMTP(t, false)
		defer stop()
		// Server name is remote even though we dial loopback; mustUseTLS keys off host.
		m := NewSMTPMailer("smtp.remote.example", addr, "", "", "no-reply@example.com", true)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := m.SendPasswordReset(ctx, "alice@example.com", pageURL, code)
		if err == nil {
			t.Fatal("remote server without STARTTLS must fail closed")
		}
		if !strings.Contains(err.Error(), "TLS required") {
			t.Errorf("expected a TLS-required error, got %q", err)
		}
		for _, secret := range []string{code, pageURL, "alice@example.com"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error must not leak secrets: %q", err)
			}
		}
	})

	t.Run("local host with requireTLS fails closed", func(t *testing.T) {
		addr, stop := startFakeSMTP(t, false)
		defer stop()
		m := NewSMTPMailer("localhost", addr, "", "", "no-reply@example.com", true)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := m.SendPasswordReset(ctx, "alice@example.com", pageURL, code)
		if err == nil {
			t.Fatal("requireTLS=true must fail closed even for a local server")
		}
		if !strings.Contains(err.Error(), "TLS required") {
			t.Errorf("expected a TLS-required error, got %q", err)
		}
	})

	t.Run("local dev exception delivers plaintext", func(t *testing.T) {
		addr, stop := startFakeSMTP(t, false)
		defer stop()
		m := NewSMTPMailer("localhost", addr, "", "", "no-reply@example.com", false)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.SendPasswordReset(ctx, "alice@example.com", pageURL, code); err != nil {
			t.Fatalf("local dev server with requireTLS=false must deliver: %v", err)
		}
	})
}
