package config

import (
	"strings"
	"testing"
)

// The weak-secret checks used to be production-only: a development install
// that copied the example file ran on the placeholder JWT secret printed in
// this repository, with every token it signed forgeable by anyone who read
// the source. Outside production the three secret checks now refuse the boot
// too, unless the install states ALLOW_INSECURE_DEV_SECRETS=true — one
// explicit act, and only where APP_ENV already opted out of the hardened mode.

func strongSecrets(c *Config) {
	c.JWT.SecretKey = "8f14e45fceea167a5a36dedd4bea2543f14e45fceea167a5a36dedd4bea25431"
	c.Secrets.EncryptionKey = "c4ca4238a0b923820dcc509a6f75849bca4238a0b923820dcc509a6f75849bcd"
	c.Secrets.CDNSigningKey = "e10adc3949ba59abbe56e057f20f883ee10adc3949ba59abbe56e057f20f883e"
}

func TestWeakSecretsRefuseTheBootOutsideProductionWithoutTheOptIn(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"
	err := c.Validate()
	if err == nil {
		t.Fatal("a development config on the placeholder secrets booted without the opt-in")
	}
	for _, name := range []string{"JWT_SECRET_KEY", "SECRETS_ENCRYPTION_KEY", "CDN_SIGNING_KEY", "ALLOW_INSECURE_DEV_SECRETS"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal must name %s so the operator knows what to set; got %q", name, err)
		}
	}
}

func TestTheOptInLetsADevelopmentInstallRunOnWeakSecrets(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"
	c.Server.AllowInsecureDevSecrets = true
	if err := c.Validate(); err != nil {
		t.Fatalf("development with the opt-in must boot on the placeholder secrets: %v", err)
	}
}

// The opt-in is scoped to the SECRET checks: a development install genuinely
// serves plain HTTP on a trusted LAN, so plaintext gRPC, an open sign-up or a
// wildcard CORS origin stay warnings there — exactly as before.
func TestOutsideProductionOnlyTheSecretChecksRefuse(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"
	strongSecrets(c)
	if problems := c.Problems(); len(problems) == 0 {
		t.Fatal("the fixture must still carry non-secret problems (plaintext gRPC, wildcard CORS) for this test to mean anything")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("strong secrets alone must satisfy a development boot: %v", err)
	}
}

// Production ignores the opt-in entirely: it is a development convenience and
// must never become a way to run a reachable server on a known key. The
// refusal still lists every problem, and the advisory says the flag did nothing.
func TestTheOptInIsIgnoredInProduction(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = EnvProduction
	c.Server.AllowInsecureDevSecrets = true
	if err := c.Validate(); err == nil {
		t.Fatal("production booted on the placeholder secrets because of a development opt-in")
	}
	if !advisoryNames(c, "ALLOW_INSECURE_DEV_SECRETS") {
		t.Error("production must say the opt-in had no effect, or an operator who set it keeps looking elsewhere")
	}
}

// The opt-in in use is itself said out loud, once, so a log reader can see
// that this process signs tokens with a published key.
func TestRunningOnTheOptInIsAnAdvisory(t *testing.T) {
	c := getDefaultConfig()
	c.Server.Environment = "development"
	c.Server.AllowInsecureDevSecrets = true
	if !advisoryNames(c, "ALLOW_INSECURE_DEV_SECRETS") {
		t.Error("a development install running on weak secrets by opt-in must log that it does")
	}
	// With strong secrets the opt-in is inert and says nothing.
	strongSecrets(c)
	if advisoryNames(c, "ALLOW_INSECURE_DEV_SECRETS") {
		t.Error("an inert opt-in must not be reported")
	}
}

func TestAllowInsecureDevSecretsIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_DEV_SECRETS", "true")
	cfg, err := ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.AllowInsecureDevSecrets {
		t.Fatal("ALLOW_INSECURE_DEV_SECRETS=true was not applied")
	}
	t.Setenv("ALLOW_INSECURE_DEV_SECRETS", "yes-please")
	cfg, err = ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.AllowInsecureDevSecrets {
		t.Fatal("an unparseable boolean must not opt in")
	}
}

func advisoryNames(c *Config, variable string) bool {
	for _, a := range c.Advisories() {
		if strings.Contains(a, variable) {
			return true
		}
	}
	return false
}
