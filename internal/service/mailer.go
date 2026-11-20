package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// ResetMailer sends password-reset emails. It is a small, consumer-defined
// interface so tests can mock delivery without any network. The reset code is
// delivered in the message body (copy/paste), never embedded in a URL, so it
// cannot leak through browser history or Referer headers (S-05). resetPageURL
// is the clean page address (no token); resetCode is the single-use raw token.
type ResetMailer interface {
	SendPasswordReset(ctx context.Context, toEmail, resetPageURL, resetCode string) error
}

// SMTPMailer implements ResetMailer with the standard library's net/smtp (no
// additional dependency). TLS is enforced (S-01): STARTTLS is used when the
// server advertises it; when it is absent the connection fails closed unless
// the host is local AND the TLS requirement is explicitly disabled (dev only).
// Credentials, the recipient, the page URL, and the reset code are never
// logged; returned errors are static and secret-safe because SMTP server
// responses can echo internal details.
type SMTPMailer struct {
	host       string
	addr       string
	username   string
	password   string
	from       string
	requireTLS bool
}

var _ ResetMailer = (*SMTPMailer)(nil)

// NewSMTPMailer builds the mailer. host is the server name (TLS verification
// and PlainAuth); addr is the host:port dial address. username/password are
// optional; from is the sender address. requireTLS defaults to true at the
// config layer; set false only for a local development SMTP server.
func NewSMTPMailer(host, addr, username, password, from string, requireTLS bool) *SMTPMailer {
	return &SMTPMailer{host: host, addr: addr, username: username, password: password, from: from, requireTLS: requireTLS}
}

// isLocalSMTPHost reports whether host is a loopback/local development server.
func isLocalSMTPHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// mustUseTLS reports whether the connection must be upgraded to TLS (failing
// closed otherwise). Plaintext is permitted only for a local dev server with
// the TLS requirement explicitly disabled; a remote server always requires TLS
// so the reset code is never sent in cleartext (S-01).
func mustUseTLS(host string, requireTLS bool) bool {
	return requireTLS || !isLocalSMTPHost(host)
}

// SendPasswordReset delivers a plain-text reset email. The context bounds the
// dial and all I/O via a connection deadline.
func (m *SMTPMailer) SendPasswordReset(ctx context.Context, toEmail, resetPageURL, resetCode string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", m.addr)
	if err != nil {
		return fmt.Errorf("password reset email: dial failed")
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("password reset email: connection failed")
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		// Certificate is verified against the host (InsecureSkipVerify=false),
		// TLS >= 1.2.
		if err := c.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("password reset email: TLS negotiation failed")
		}
	} else if mustUseTLS(m.host, m.requireTLS) {
		// S-01: fail closed — never send the reset code in cleartext.
		return fmt.Errorf("password reset email: TLS required but server does not offer STARTTLS")
	}
	// Plaintext path is reached only for a local dev server with requireTLS=false.
	// smtp.PlainAuth additionally refuses to send credentials over an unencrypted
	// non-local connection.
	if m.username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fmt.Errorf("password reset email: authentication failed")
		}
	}
	if err := c.Mail(m.from); err != nil {
		return fmt.Errorf("password reset email: sender rejected")
	}
	if err := c.Rcpt(toEmail); err != nil {
		return fmt.Errorf("password reset email: recipient rejected")
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("password reset email: data failed")
	}
	if _, err := w.Write([]byte(resetMessage(m.from, toEmail, resetPageURL, resetCode))); err != nil {
		_ = w.Close()
		return fmt.Errorf("password reset email: write failed")
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("password reset email: commit failed")
	}
	_ = c.Quit()
	return nil
}

// resetMessage builds the RFC 5322 plain-text message. The reset code appears
// only in the body (copy/paste into the reset page); the page URL carries no
// token, avoiding browser-history and Referer leakage (S-05). Nothing here is
// logged.
func resetMessage(from, to, pageURL, code string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: Reset your password\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString("We received a request to reset your password.\r\n\r\n")
	b.WriteString("Open the reset page below and enter the reset code. The code is\r\n")
	b.WriteString("single-use and will expire shortly.\r\n\r\n")
	b.WriteString("Reset page: " + pageURL + "\r\n")
	b.WriteString("Reset code: " + code + "\r\n\r\n")
	b.WriteString("If you did not request this, you can safely ignore this email.\r\n")
	return b.String()
}
