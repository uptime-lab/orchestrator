package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The boot entitlement follows the credential-file discipline: ENTITLEMENT_FILE
// is read first, a trailing newline is dropped, and a named file that cannot be
// read leaves the value UNSET rather than falling back to the environment — the
// air-gap path must not quietly run under a stale copy.
func TestEntitlementIsReadFromItsFileFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entitlement")
	if err := os.WriteFile(path, []byte("entl1.from-file.sig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENTITLEMENT", "entl1.from-env.sig")
	t.Setenv("ENTITLEMENT_FILE", path)
	t.Setenv("ENTITLEMENT_SUBSCRIPTION_URL", "https://example.com/plans")

	cfg, err := ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Entitlement.Document != "entl1.from-file.sig" {
		t.Errorf("document = %q, want the file's value without its newline", cfg.Entitlement.Document)
	}
	if cfg.Entitlement.SubscriptionURL != "https://example.com/plans" {
		t.Errorf("subscription url = %q", cfg.Entitlement.SubscriptionURL)
	}

	t.Setenv("ENTITLEMENT_FILE", filepath.Join(dir, "missing"))
	cfg, err = ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Entitlement.Document != "" {
		t.Errorf("an unreadable file fell back to the environment copy: %q", cfg.Entitlement.Document)
	}
}
