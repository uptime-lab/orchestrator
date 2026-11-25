package sql

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

func TestEncryptProvisioningPasswordRoundTrip(t *testing.T) {
	enc := crypto.NewEncryptor("primary")

	stored, marker, err := encryptProvisioningPassword(enc, "s3cret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if marker != provisioningPasswordEncAES {
		t.Fatalf("marker = %d, want %d", marker, provisioningPasswordEncAES)
	}
	if stored == "s3cret" {
		t.Fatal("the stored value must not be the plaintext")
	}

	got, unreadable := decryptProvisioningPassword(enc, stored, marker, "test")
	if unreadable {
		t.Fatal("a value written with the configured key must not read back as unreadable")
	}
	if got != "s3cret" {
		t.Fatalf("decrypt returned %q, want %q", got, "s3cret")
	}
}

// An empty password is stored as empty plaintext: there is nothing to protect,
// and "unset" has to stay queryable (the backfill and the rotation lists both
// filter on default_password <> '').
func TestEncryptProvisioningPasswordLeavesEmptyUnmarked(t *testing.T) {
	stored, marker, err := encryptProvisioningPassword(crypto.NewEncryptor("primary"), "")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if stored != "" || marker != 0 {
		t.Fatalf("empty password stored as (%q, %d), want (\"\", 0)", stored, marker)
	}
}

// Legacy rows predate the marker column and hold real plaintext. They must be
// returned verbatim rather than fed to Decrypt — this is the accept-on-read
// half of the migration, and getting it wrong locks every unmigrated install
// out of its own devices.
func TestDecryptProvisioningPasswordAcceptsLegacyPlaintext(t *testing.T) {
	enc := crypto.NewEncryptor("primary")
	got, unreadable := decryptProvisioningPassword(enc, "plain-old-password", 0, "test")
	if unreadable {
		t.Fatal("a legacy plaintext row must not be reported as unreadable")
	}
	if got != "plain-old-password" {
		t.Fatalf("legacy read returned %q", got)
	}
}

// A plaintext password that happens to start with the ciphertext prefix is the
// reason the marker is a column and not a byte prefix. Sniffing the value would
// classify this row as encrypted and then fail to decrypt it.
func TestDecryptProvisioningPasswordLegacyValueLookingLikeCiphertext(t *testing.T) {
	enc := crypto.NewEncryptor("primary")
	const looksEncrypted = "v1:notreallyciphertextbutlonger0000000000"
	got, unreadable := decryptProvisioningPassword(enc, looksEncrypted, 0, "test")
	if unreadable || got != looksEncrypted {
		t.Fatalf("marker 0 must win over the value's shape; got (%q, %v)", got, unreadable)
	}
}

// A row written under a key nobody configured any more: report it, do not
// silently degrade it to "no password set", because the two mean different
// things at the provisioning boundary.
func TestDecryptProvisioningPasswordReportsUnreadable(t *testing.T) {
	stranger := crypto.NewEncryptor("a-key-nobody-configured")
	orphan, _ := stranger.Encrypt("device password")

	got, unreadable := decryptProvisioningPassword(crypto.NewEncryptor("primary"), orphan, provisioningPasswordEncAES, "test")
	if !unreadable {
		t.Fatal("a ciphertext no configured key decrypts must be reported as unreadable")
	}
	if got != "" {
		t.Fatalf("an unreadable value must not leak its ciphertext to the caller; got %q", got)
	}
}

// During a rotation the old key stays in the fallback set, so rows written
// under it must still read.
func TestDecryptProvisioningPasswordUsesFallbackKeys(t *testing.T) {
	old := crypto.NewEncryptor("old")
	stored, _ := old.Encrypt("device password")

	rotating := crypto.NewEncryptorWithFallbacks("new", []string{"old"})
	got, unreadable := decryptProvisioningPassword(rotating, stored, provisioningPasswordEncAES, "test")
	if unreadable || got != "device password" {
		t.Fatalf("a fallback key must decrypt; got (%q, %v)", got, unreadable)
	}
}
