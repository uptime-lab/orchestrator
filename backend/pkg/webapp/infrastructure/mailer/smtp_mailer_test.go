package mailer

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func mailerWith(cfg config.EmailConfig) *SMTPMailer {
	return NewSMTPMailer(cfg, slog.Default())
}

// Announcing "localhost" makes some relays (Google Workspace's among them) withhold the STARTTLS
// capability entirely, and the connection then dies with a bare EOF. The
// sender's own domain is what must go on the wire.
func TestHeloNameUsesTheSenderDomain(t *testing.T) {
	for _, tc := range []struct {
		from string
		want string
	}{
		{"noreply@orchestrator.sh", "orchestrator.sh"},
		{"Bounce+tag@sub.example.com", "sub.example.com"},
		{"malformed-no-at", "localhost"},
		{"trailing@", "localhost"},
	} {
		got := mailerWith(config.EmailConfig{FromAddress: tc.from}).heloName()
		if got != tc.want {
			t.Errorf("heloName() for %q = %q, want %q", tc.from, got, tc.want)
		}
	}
}

// A newline smuggled through a display name or subject would otherwise let the
// caller inject arbitrary headers or extra recipients.
func TestBuildMessageStripsHeaderInjection(t *testing.T) {
	m := mailerWith(config.EmailConfig{
		FromAddress: "noreply@orchestrator.sh",
		FromName:    "Orchestrator\r\nBcc: attacker@evil.example",
	})

	msg := m.buildMessage("victim@example.com\r\nBcc: attacker@evil.example",
		"Subject\r\nX-Injected: yes", "body")

	head, body, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatal("message has no header/body separator")
	}
	// The payload may survive as inert text inside a header VALUE — what must
	// never happen is a new header LINE, which is what an injection needs.
	for _, line := range strings.Split(head, "\r\n") {
		name, _, _ := strings.Cut(line, ":")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "bcc", "x-injected":
			t.Errorf("injected header line survived: %q", line)
		}
	}
	if !strings.HasPrefix(body, "body") {
		t.Errorf("body malformed: %q", body)
	}
	// Go's net/smtp independently rejects CR/LF in the envelope, so the
	// smuggled address can never become a real recipient either.
}

func TestConfiguredRequiresHostAndSender(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.EmailConfig
		want bool
	}{
		{"complete", config.EmailConfig{Enabled: true, Host: "smtp-relay.gmail.com", FromAddress: "a@b.c"}, true},
		{"disabled", config.EmailConfig{Host: "smtp-relay.gmail.com", FromAddress: "a@b.c"}, false},
		{"no host", config.EmailConfig{Enabled: true, FromAddress: "a@b.c"}, false},
		{"no sender", config.EmailConfig{Enabled: true, Host: "smtp-relay.gmail.com"}, false},
	} {
		if got := tc.cfg.Configured(); got != tc.want {
			t.Errorf("%s: Configured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAddrDefaultsToSubmissionPort(t *testing.T) {
	if got := (config.EmailConfig{Host: "smtp-relay.gmail.com"}).Addr(); got != "smtp-relay.gmail.com:587" {
		t.Errorf("Addr() = %q, want the submission port by default", got)
	}
	if got := (config.EmailConfig{Host: "mail.example.com", Port: 2525}).Addr(); got != "mail.example.com:2525" {
		t.Errorf("Addr() = %q, want the configured port", got)
	}
}
