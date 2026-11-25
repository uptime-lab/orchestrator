package config

import (
	"strings"
	"testing"
	"time"
)

// SERVER_OPEN_REGISTRATION is off unless asked for in a spelling the loader
// recognises; a typo must not open sign-up.
func TestOpenRegistrationFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Server.OpenRegistration {
		t.Fatal("open registration is on by default")
	}

	t.Setenv("SERVER_OPEN_REGISTRATION", "yes please")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.Server.OpenRegistration {
		t.Fatal("an unparseable value opened registration")
	}

	t.Setenv("SERVER_OPEN_REGISTRATION", "true")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if !c.Server.OpenRegistration {
		t.Fatal("SERVER_OPEN_REGISTRATION=true left registration closed")
	}

	t.Setenv("SERVER_OPEN_REGISTRATION", "false")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.Server.OpenRegistration {
		t.Fatal("SERVER_OPEN_REGISTRATION=false did not close it again")
	}
}

// withMail gives a config a working mailer and a link origin.
func withMail(cfg *Config) {
	cfg.Email = EmailConfig{Enabled: true, Host: "smtp.example.test", FromAddress: "noreply@example.test", StartTLS: true}
	cfg.Server.AppBaseURL = "https://app.example.test"
}

func advisoriesNaming(advisories []string, needle string) int {
	n := 0
	for _, a := range advisories {
		if strings.Contains(a, needle) {
			n++
		}
	}
	return n
}

// Open registration with no account ceiling is an unbounded cost, and a
// code it overrides is unused: both are said at start, neither refuses the
// boot.
func TestOpenRegistrationAdvisories(t *testing.T) {
	cfg := productionReadyConfig() // carries an invite code
	withMail(cfg)
	cfg.Server.OpenRegistration = true
	cfg.Server.MaxAccounts = 0

	advisories := cfg.Advisories()
	if advisoriesNaming(advisories, "SERVER_MAX_ACCOUNTS=0") != 1 {
		t.Errorf("no unbounded-cost advisory in %v", advisories)
	}
	if advisoriesNaming(advisories, "SERVER_INVITE_CODE is set but unused") != 1 {
		t.Errorf("no unused-code advisory in %v", advisories)
	}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("Problems() = %v, want none: open registration is the operator's call", problems)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	cfg.Server.MaxAccounts = 50
	cfg.Server.InviteCode = ""
	if advisories := cfg.Advisories(); len(advisories) != 0 {
		t.Errorf("bounded open registration with no code: Advisories() = %v, want none", advisories)
	}

	// Off: neither coupling exists, whatever the ceiling and the code say.
	cfg.Server.OpenRegistration = false
	cfg.Server.MaxAccounts = 0
	cfg.Server.InviteCode = "a-code"
	if advisories := cfg.Advisories(); len(advisories) != 0 {
		t.Errorf("open registration off: Advisories() = %v, want none", advisories)
	}
}

// Open registration admits only accounts that confirm their address, so an
// install that cannot mail the link is told at start that sign-up is closed.
func TestOpenRegistrationWithoutMailIsAnAdvisory(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.Server.InviteCode = ""
	cfg.Server.MaxAccounts = 10
	cfg.Server.OpenRegistration = true
	if n := advisoriesNaming(cfg.Advisories(), "cannot send the confirmation mail"); n != 1 {
		t.Fatalf("no mail: %d advisories naming it in %v, want 1", n, cfg.Advisories())
	}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Fatalf("Problems() = %v, want none", problems)
	}
	withMail(cfg)
	if n := advisoriesNaming(cfg.Advisories(), "cannot send the confirmation mail"); n != 0 {
		t.Fatalf("with mail the advisory still fires: %v", cfg.Advisories())
	}
	cfg.Server.AppBaseURL = ""
	if cfg.CanVerifyEmail() {
		t.Fatal("no link origin, yet the install claims it can verify")
	}
}

// The two bounds are armed by default and have no "unlimited".
func TestOpenRegistrationBoundsFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Server.OpenRegistrationMaxPending != 20 || c.Server.OpenRegistrationPendingHours != 48 {
		t.Fatalf("defaults = %d pending, %d h; want 20, 48", c.Server.OpenRegistrationMaxPending, c.Server.OpenRegistrationPendingHours)
	}
	t.Setenv("SERVER_OPEN_REGISTRATION_MAX_PENDING", "5")
	t.Setenv("SERVER_OPEN_REGISTRATION_PENDING_HOURS", "12")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatal(err)
	}
	if c.Server.OpenRegistrationMaxPending != 5 || c.Server.OpenRegistrationPendingWindow() != 12*time.Hour {
		t.Fatalf("after env: %d pending, %v", c.Server.OpenRegistrationMaxPending, c.Server.OpenRegistrationPendingWindow())
	}
	for _, rejected := range []string{"0", "-1", "lots"} {
		t.Setenv("SERVER_OPEN_REGISTRATION_MAX_PENDING", rejected)
		t.Setenv("SERVER_OPEN_REGISTRATION_PENDING_HOURS", rejected)
		if _, err := ApplyEnvironment(c); err != nil {
			t.Fatal(err)
		}
		if c.Server.OpenRegistrationMaxPending != 5 || c.Server.OpenRegistrationPendingHours != 12 {
			t.Fatalf("value %q changed a bound to %d/%d", rejected, c.Server.OpenRegistrationMaxPending, c.Server.OpenRegistrationPendingHours)
		}
	}
}
