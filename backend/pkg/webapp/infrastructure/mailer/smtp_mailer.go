// Package mailer is the outbound SMTP adapter.
//
// It works with relays that authorize by sender IP rather than by credentials
// (for example the Google Workspace SMTP relay), where there is nothing secret
// in this configuration at all, and with authenticated submission hosts:
// supply a username and password.
package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// dialTimeout bounds a hung relay: sending mail must never wedge the request
// that triggered it.
const dialTimeout = 15 * time.Second

// SMTPMailer sends mail over SMTP with STARTTLS.
type SMTPMailer struct {
	cfg    config.EmailConfig
	logger *slog.Logger
}

func NewSMTPMailer(cfg config.EmailConfig, logger *slog.Logger) *SMTPMailer {
	return &SMTPMailer{cfg: cfg, logger: logger.With("component", "mailer")}
}

// Send delivers a plain-text message. The body is deliberately text-only: a
// verification mail is one sentence and a link, and text/plain is both the most
// deliverable form and the one least likely to be mangled by a client.
func (m *SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	if !m.cfg.Configured() {
		return fmt.Errorf("smtp mailer is not configured")
	}

	msg := m.buildMessage(to, subject, body)

	// net/smtp has no context support, so the deadline is applied to the dial
	// and enforced by the connection's own timeouts thereafter.
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", m.cfg.Addr())
	if err != nil {
		return fmt.Errorf("dialing smtp host: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	}

	host, _, splitErr := net.SplitHostPort(m.cfg.Addr())
	if splitErr != nil {
		conn.Close()
		return fmt.Errorf("parsing smtp host: %w", splitErr)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("starting smtp session: %w", err)
	}
	defer client.Close()

	// Introduce ourselves with a real domain. net/smtp otherwise says
	// "EHLO localhost", and some relays (the Google Workspace relay among them)
	// respond to that by not advertising STARTTLS at all — the connection then
	// dies with a bare EOF
	// the moment TLS is attempted, which reads like a network fault rather than
	// a rejected greeting.
	if err := client.Hello(m.heloName()); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}

	if m.cfg.StartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starting TLS: %w", err)
		}
	}

	// The Workspace relay authorizes by IP and offers no AUTH mechanism, so
	// authenticate only when credentials were actually supplied.
	if m.cfg.Username != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(m.cfg.FromAddress); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing message: %w", err)
	}
	return client.Quit()
}

// buildMessage assembles the RFC 5322 message. Header values are sanitized
// against CRLF injection: a newline smuggled through a display name or subject
// would let a caller inject arbitrary headers or extra recipients.
func (m *SMTPMailer) buildMessage(to, subject, body string) string {
	from := sanitizeHeader(m.cfg.FromAddress)
	if name := sanitizeHeader(m.cfg.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", name), from)
	}
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + sanitizeHeader(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", sanitizeHeader(subject)) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	// Transactional mail must not be auto-replied to or filed as bulk.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.String()
}

// heloName is the domain announced in EHLO. The sender's own domain is the
// honest answer and the one relays expect; "localhost" is never acceptable.
func (m *SMTPMailer) heloName() string {
	if at := strings.LastIndex(m.cfg.FromAddress, "@"); at >= 0 {
		if domain := strings.TrimSpace(m.cfg.FromAddress[at+1:]); domain != "" {
			return domain
		}
	}
	return "localhost"
}

func sanitizeHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(value))
}
