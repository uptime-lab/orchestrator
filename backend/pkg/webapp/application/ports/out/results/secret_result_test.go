package results

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// An unrecoverable value and an empty one are otherwise the same JSON, and the
// operator's next step differs: re-set the entry, or leave it alone.
func TestNewSecretResultReportsAnUnreadableValueAsSet(t *testing.T) {
	res := NewSecretResult(&secret.Secret{
		ID: uuid.New(), Name: "orphan", Kind: secret.KindOpaque, Sensitive: true, ValueUnreadable: true,
	})
	if !res.Unreadable {
		t.Error("unreadable = false for a value that decrypts with no configured key")
	}
	if !res.HasValue {
		t.Error("has_value = false for an unreadable value; it reads as 'never set'")
	}
	if res.Value != nil {
		t.Errorf("unreadable secret carries a value %q", *res.Value)
	}
}

func TestNewSecretResultLeavesUnreadableOffAHealthySecret(t *testing.T) {
	res := NewSecretResult(&secret.Secret{
		ID: uuid.New(), Name: "fine", Kind: secret.KindEnv, Key: "OK", Value: "v",
	})
	if res.Unreadable {
		t.Error("a readable secret is reported unreadable")
	}
	if res.Value == nil || *res.Value != "v" {
		t.Errorf("non-sensitive secret must expose its value, got %v", res.Value)
	}
}
