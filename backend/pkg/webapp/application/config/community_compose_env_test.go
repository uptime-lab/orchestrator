package config

import "testing"

// The checker reads what compose would run, not the first matching text: a
// second assignment after the pinned line, and a pinned default that survives
// only in a comment above a literal, both arm the failsafe and must not pass.
func TestBackendEnvDefaultCannotBeFooledByTheText(t *testing.T) {
	const name = "PROVISIONING_BOOT_FAILSAFE_THRESHOLD"
	const pinned = "services:\n  backend:\n    environment:\n" +
		"      - " + name + "=${" + name + ":-0}\n"
	if v, err := backendEnvDefault(pinned, name); err != nil || v != "0" {
		t.Fatalf("the pinned spelling: %q, %v", v, err)
	}

	overridden := pinned + "      - " + name + "=3\n"
	if v, err := backendEnvDefault(overridden, name); err == nil {
		t.Errorf("a second assignment after the pinned line was read as %q", v)
	}

	commented := "services:\n  backend:\n    environment:\n" +
		"      # - " + name + "=${" + name + ":-0}\n" +
		"      - " + name + "=3\n"
	if v, err := backendEnvDefault(commented, name); err != nil || v != "3" {
		t.Errorf("a pinned default left only in a comment: got %q, %v; want the literal 3", v, err)
	}

	elsewhere := "x-note: ${" + name + ":-0}\nservices:\n  backend:\n    environment:\n" +
		"      - " + name + "=${OTHER:-0}\n"
	if v, err := backendEnvDefault(elsewhere, name); err == nil {
		t.Errorf("a substitution of another variable was read as %q", v)
	}
}
