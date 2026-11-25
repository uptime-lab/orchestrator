package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	e := NewEncryptor("some-short-passphrase")
	for _, plaintext := range []string{"", "hello", "p@ssw0rd with spaces and unicode ☃"} {
		ct, err := e.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("encrypt(%q): %v", plaintext, err)
		}
		if plaintext != "" && ct == plaintext {
			t.Errorf("ciphertext should differ from plaintext for %q", plaintext)
		}
		got, err := e.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt(%q): %v", plaintext, err)
		}
		if got != plaintext {
			t.Errorf("round trip mismatch: got %q, want %q", got, plaintext)
		}
	}
}

func TestEncryptNonceRandomness(t *testing.T) {
	e := NewEncryptor("key")
	a, _ := e.Encrypt("same")
	b, _ := e.Encrypt("same")
	if a == b {
		t.Error("expected different ciphertexts due to random nonce")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	ct, _ := NewEncryptor("key-a").Encrypt("secret")
	if _, err := NewEncryptor("key-b").Decrypt(ct); err == nil {
		t.Error("expected decryption with wrong key to fail")
	}
}

func TestDecryptTooShort(t *testing.T) {
	e := NewEncryptor("key")
	if _, err := e.Decrypt("AAAA"); err == nil {
		t.Error("expected error for short ciphertext")
	}
}

func TestEncryptEmitsVersionPrefix(t *testing.T) {
	ct, err := NewEncryptor("key").Encrypt("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(ct, versionPrefix) {
		t.Errorf("expected ciphertext to start with %q, got %q", versionPrefix, ct)
	}
}

// TestDecryptLegacyUnprefixedCiphertext ensures rows encrypted before the
// version prefix was introduced still decrypt.
func TestDecryptLegacyUnprefixedCiphertext(t *testing.T) {
	e := NewEncryptor("key")
	ct, err := e.Encrypt("legacy-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	legacy := strings.TrimPrefix(ct, versionPrefix)
	got, err := e.Decrypt(legacy)
	if err != nil {
		t.Fatalf("decrypt legacy: %v", err)
	}
	if got != "legacy-value" {
		t.Errorf("legacy round trip mismatch: got %q", got)
	}
}

func TestFallbackKeyRotation(t *testing.T) {
	oldEnc := NewEncryptor("old-key")
	ct, err := oldEnc.Encrypt("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	// New primary, old as fallback: must still decrypt the old ciphertext.
	rotated := NewEncryptorWithFallbacks("new-key", []string{"old-key"})
	got, err := rotated.Decrypt(ct)
	if err != nil || got != "hunter2" {
		t.Fatalf("fallback decrypt failed: got %q err %v", got, err)
	}
	// Old ciphertext is NOT primary-encrypted yet.
	if rotated.EncryptedWithPrimary(ct) {
		t.Error("old ciphertext must not report as primary-encrypted")
	}
	// Re-encrypt under the new primary; now it is primary-encrypted and the
	// old key alone can no longer read it.
	ct2, _ := rotated.Encrypt(got)
	if !rotated.EncryptedWithPrimary(ct2) {
		t.Error("re-encrypted ciphertext must be primary-encrypted")
	}
	if _, err := oldEnc.Decrypt(ct2); err == nil {
		t.Error("old key must NOT decrypt data re-encrypted under the new key")
	}
}
