package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretEnvPrefersTheFileAndStripsTheNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jwt")
	// A trailing newline is what every editor and `echo` adds, and a credential
	// carrying one is a credential that does not match.
	if err := os.WriteFile(path, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	t.Setenv("TEST_CREDENTIAL", "from-the-environment")
	t.Setenv("TEST_CREDENTIAL_FILE", path)

	if got := secretEnv("TEST_CREDENTIAL"); got != "from-the-file" {
		t.Fatalf("read %q, want the file's contents without its newline", got)
	}
}

func TestSecretEnvFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("TEST_CREDENTIAL", "from-the-environment")
	if got := secretEnv("TEST_CREDENTIAL"); got != "from-the-environment" {
		t.Fatalf("read %q, want the environment value when no file is named", got)
	}
}

// The trap worth pinning: an unreadable file must NOT fall back to the
// environment. A named file is an explicit statement about where the credential
// lives, and quietly using a stale environment copy would leave an operator
// believing a rotation had taken effect when it had not.
func TestSecretEnvDoesNotFallBackWhenTheFileIsUnreadable(t *testing.T) {
	t.Setenv("TEST_CREDENTIAL", "the-old-value")
	t.Setenv("TEST_CREDENTIAL_FILE", filepath.Join(t.TempDir(), "absent"))

	if got := secretEnv("TEST_CREDENTIAL"); got != "" {
		t.Fatalf("read %q, want empty — a named-but-unreadable file must not silently use the environment", got)
	}
}

// An empty _FILE is "not configured", not "read the file called empty string".
func TestSecretEnvIgnoresABlankFilePath(t *testing.T) {
	t.Setenv("TEST_CREDENTIAL", "from-the-environment")
	t.Setenv("TEST_CREDENTIAL_FILE", "   ")
	if got := secretEnv("TEST_CREDENTIAL"); got != "from-the-environment" {
		t.Fatalf("read %q, want the environment value", got)
	}
}
