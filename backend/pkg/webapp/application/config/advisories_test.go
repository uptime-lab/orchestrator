package config

import (
	"strings"
	"testing"
)

// productionReadyConfig is a config that Problems() finds nothing wrong with,
// so a test can tell an advisory apart from a fail-closed condition.
func productionReadyConfig() *Config {
	cfg := &Config{}
	cfg.Server.Environment = "production"
	cfg.Server.InviteCode = "an-invite-code"
	cfg.Server.CORS.Enabled = true
	cfg.Server.CORS.AllowedOrigins = []string{"https://app.example.test"}
	cfg.JWT.SecretKey = strings.Repeat("j", 48)
	cfg.Secrets.EncryptionKey = strings.Repeat("k", 48)
	// Set, because an UNSET key is a production problem
	// (cdn_signing_key_test.go), and the failsafe tests below count advisories
	// against a clean baseline.
	cfg.Secrets.CDNSigningKey = strings.Repeat("c", 48)
	cfg.GRPC.TLSTerminatedExternally = true
	return cfg
}

func TestAdvisoriesNeverBecomeProblems(t *testing.T) {
	cfg := productionReadyConfig()
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Fatalf("baseline config should be clean, got: %v", problems)
	}

	// The armed failsafe is an advisory. It must NOT make production refuse to
	// boot: the backend runs under `restart: always`, so a promoted advisory is
	// a crash loop, and a development install cannot catch it (it only logs
	// what production refuses on).
	cfg.Provisioning.BootFailsafeThreshold = 3

	advisories := cfg.Advisories()
	if len(advisories) != 1 || !strings.Contains(advisories[0], "PROVISIONING_BOOT_FAILSAFE_THRESHOLD") {
		t.Fatalf("Advisories() = %v, want one naming PROVISIONING_BOOT_FAILSAFE_THRESHOLD", advisories)
	}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("Problems() = %v, want none: an advisory must never be a fail-closed condition", problems)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil: an armed failsafe must not refuse to boot production", err)
	}
}

func TestAdvisoriesSilentWhenFailsafeIsOff(t *testing.T) {
	cfg := productionReadyConfig()
	cfg.Provisioning.BootFailsafeThreshold = 0

	if advisories := cfg.Advisories(); len(advisories) != 0 {
		t.Errorf("Advisories() = %v, want none with the failsafe disabled", advisories)
	}
}
