package sql

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

func TestEncryptDeviceTokenRoundTrip(t *testing.T) {
	enc := crypto.NewEncryptor("primary")
	token := uuid.New().String()

	stored, marker, err := encryptDeviceToken(enc, token)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if marker != deviceTokenEncAES {
		t.Fatalf("marker = %d, want %d", marker, deviceTokenEncAES)
	}
	if stored == token {
		t.Fatal("the stored value must not be the plaintext")
	}
	got, unreadable := decryptDeviceToken(enc, stored, marker, "test")
	if unreadable || got != token {
		t.Fatalf("decrypt = %q unreadable=%v, want the token back", got, unreadable)
	}
}

// A legacy plaintext row (marker 0) reads back verbatim: the backfill migrates
// it, and until then the device must keep authenticating.
func TestDecryptDeviceTokenPassesLegacyPlaintextThrough(t *testing.T) {
	got, unreadable := decryptDeviceToken(crypto.NewEncryptor("primary"), "legacy-token", 0, "test")
	if unreadable || got != "legacy-token" {
		t.Fatalf("legacy read = %q unreadable=%v", got, unreadable)
	}
}

// A ciphertext no configured key can read comes back empty WITH the flag: the
// connect path then refuses that device (the honest outcome of a lost key),
// and the flag is what keeps writes from replacing the ciphertext.
func TestDecryptDeviceTokenMarksUnreadable(t *testing.T) {
	stored, marker, err := encryptDeviceToken(crypto.NewEncryptor("primary"), "tok")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, unreadable := decryptDeviceToken(crypto.NewEncryptor("some-other-key"), stored, marker, "test")
	if !unreadable || got != "" {
		t.Fatalf("wrong-key read = %q unreadable=%v, want empty+flag", got, unreadable)
	}
}

// The full-row write path must leave an unreadable token's columns alone: an
// encrypted blank would permanently unauthenticate the device, while the
// preserved ciphertext is recoverable by a key rotation.
func TestFromDomainOmitsUnreadableTokenColumns(t *testing.T) {
	repo := &NodeRepository{enc: crypto.NewEncryptor("primary")}
	n := &node.Node{ID: uuid.New(), UniqueToken: "", UniqueTokenUnreadable: true}
	model := &NodeModel{}
	omit, err := repo.fromDomain(model, n)
	if err != nil {
		t.Fatalf("fromDomain: %v", err)
	}
	found := 0
	for _, col := range omit {
		if col == "unique_token" || col == "unique_token_enc" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("omit = %v, want both unique_token columns preserved", omit)
	}
}
