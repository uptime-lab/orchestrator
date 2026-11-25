package config

import (
	"strings"
	"testing"
)

// The whole point of the split: with CDN_SIGNING_KEY set, the capability HMACs
// stop deriving from the JWT secret, so the CDN key rotates on its own instead
// of taking every session with it.
func TestCDNSigningSecretPrefersTheDedicatedKey(t *testing.T) {
	cfg := &Config{}
	cfg.JWT.SecretKey = "jwt-secret-value"
	cfg.Secrets.CDNSigningKey = "dedicated-cdn-key"
	if got := cfg.CDNSigningSecret(); got != "dedicated-cdn-key" {
		t.Fatalf("CDNSigningSecret() = %q, want the dedicated key", got)
	}
}

// Unset falls back to the JWT secret, which is what every install ran before
// the split — the upgrade must not invalidate anything by itself.
func TestCDNSigningSecretFallsBackToTheJWTSecret(t *testing.T) {
	cfg := &Config{}
	cfg.JWT.SecretKey = "jwt-secret-value"
	if got := cfg.CDNSigningSecret(); got != "jwt-secret-value" {
		t.Fatalf("CDNSigningSecret() = %q, want the JWT secret fallback", got)
	}
	// Whitespace is not a key: a value that is only blanks must fall back too,
	// or a stray env line silently re-keys every capability.
	cfg.Secrets.CDNSigningKey = "   "
	if got := cfg.CDNSigningSecret(); got != "jwt-secret-value" {
		t.Fatalf("CDNSigningSecret() = %q, want the fallback for a blank key", got)
	}
}

func hasCDNProblem(c *Config) bool {
	for _, p := range c.Problems() {
		if strings.HasPrefix(p, "CDN_SIGNING_KEY") {
			return true
		}
	}
	return false
}

// A weak dedicated key is a production problem.
func TestWeakCDNSigningKeyIsAProductionProblem(t *testing.T) {
	weak := productionReadyConfig()
	weak.Secrets.CDNSigningKey = "short"
	if !hasCDNProblem(weak) {
		t.Fatal("a weak CDN_SIGNING_KEY must be reported in production")
	}
}

// An unset CDN_SIGNING_KEY is a PROBLEM — production refuses to boot on it —
// not an advisory that lets an install run on the JWT-derived fallback until
// the key is seeded. This test is what pins that promotion.
func TestUnsetCDNSigningKeyIsAProductionProblem(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.Secrets.CDNSigningKey = ""

	if !hasCDNProblem(cfg) {
		t.Fatalf("Problems() = %v, want one starting with CDN_SIGNING_KEY", cfg.Problems())
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "CDN_SIGNING_KEY") {
		t.Fatalf("Validate() = %v, want a refusal naming CDN_SIGNING_KEY", err)
	}
	// A blank value is an unset value (CDNSigningSecret already treats it so),
	// and the problem must agree, or a stray whitespace line satisfies it.
	cfg.Secrets.CDNSigningKey = "   "
	if !hasCDNProblem(cfg) {
		t.Fatalf("Problems() = %v with a blank key, want the CDN problem", cfg.Problems())
	}
	// No advisory any more: one line per condition, and Problems() is the one
	// the caller logs as a warning outside production.
	for _, a := range cfg.Advisories() {
		if strings.HasPrefix(a, "CDN_SIGNING_KEY") {
			t.Fatalf("Advisories() = %v, want no CDN_SIGNING_KEY advisory now that it is a problem", cfg.Advisories())
		}
	}
}

// Outside production the same problem is computed and, being one of the
// secret checks, refuses the boot until the install opts in with
// ALLOW_INSECURE_DEV_SECRETS — a development install that must start on a
// bare config says so once, and the JWT-derived fallback then carries it.
func TestUnsetCDNSigningKeyRefusesADevelopmentBootUntilOptedOut(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.Server.Environment = "development"
	cfg.Secrets.CDNSigningKey = ""
	if !hasCDNProblem(cfg) {
		t.Fatalf("Problems() = %v, want the CDN problem listed in every environment", cfg.Problems())
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() = nil in development with no CDN key and no opt-in; the fallback key is in the source")
	}
	cfg.Server.AllowInsecureDevSecrets = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v in development with the opt-in, want nil", err)
	}
}

// Once the key is set neither list mentions it.
func TestSetCDNSigningKeyIsSilent(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.Secrets.CDNSigningKey = strings.Repeat("c", 48)
	if hasCDNProblem(cfg) {
		t.Fatalf("Problems() = %v, want none about CDN_SIGNING_KEY once it is set", cfg.Problems())
	}
	for _, a := range cfg.Advisories() {
		if strings.HasPrefix(a, "CDN_SIGNING_KEY") {
			t.Fatalf("Advisories() = %v, want none about CDN_SIGNING_KEY once it is set", cfg.Advisories())
		}
	}
}
