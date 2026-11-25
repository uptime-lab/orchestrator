package adoption_code

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewProducesHashedShortLivedCode(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	entity, code, err := New(accountID, nodeID)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if !strings.HasPrefix(code, Prefix) {
		t.Errorf("code %q missing prefix %q", code, Prefix)
	}
	if !LooksLikeCode(code) {
		t.Errorf("New produced a code that fails LooksLikeCode: %q", code)
	}
	// Only the hash is stored, and it matches the plaintext.
	if entity.CodeHash != Hash(code) {
		t.Error("stored hash does not match the plaintext code")
	}
	if entity.CodeHash == code {
		t.Error("plaintext code must not be stored")
	}
	if entity.AccountID != accountID || entity.NodeID != nodeID {
		t.Error("entity does not carry the requested account/node")
	}
	ttl := time.Until(entity.ExpiresAt)
	if ttl <= 0 || ttl > DefaultTTL+time.Minute {
		t.Errorf("unexpected TTL %v", ttl)
	}
}

func TestNewProducesUniqueCodes(t *testing.T) {
	_, a, _ := New(uuid.New(), uuid.New())
	_, b, _ := New(uuid.New(), uuid.New())
	if a == b {
		t.Error("two mints produced the same code")
	}
}

func TestLooksLikeCode(t *testing.T) {
	_, valid, _ := New(uuid.New(), uuid.New())
	cases := map[string]bool{
		valid:                     true,
		"  " + valid + "  ":       true, // trimmed
		"ac_short":                false,
		"mt_deadbeef":             false,
		"ac_" + strings.Repeat("z", 32): false, // non-hex
		"":                        false,
		"eyJub2RlX2lkIjoi":        false, // legacy base64 blob start
	}
	for in, want := range cases {
		if got := LooksLikeCode(in); got != want {
			t.Errorf("LooksLikeCode(%q) = %v, want %v", in, got, want)
		}
	}
}
