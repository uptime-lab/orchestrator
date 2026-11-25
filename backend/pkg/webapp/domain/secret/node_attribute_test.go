package secret

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateAttributeKey(t *testing.T) {
	valid := []string{"ci_agent_token", "TOKEN", "_x", "a1", "A_B_C", "x123"}
	for _, k := range valid {
		if err := ValidateAttributeKey(k); err != nil {
			t.Errorf("expected %q valid, got %v", k, err)
		}
	}
	invalid := []string{"", "1abc", "has.dot", "has-dash", "has space", "spécial"}
	for _, k := range invalid {
		if err := ValidateAttributeKey(k); err == nil {
			t.Errorf("expected %q invalid", k)
		}
	}
	// Over the length cap.
	long := make([]byte, maxAttributeKeyLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateAttributeKey(string(long)); err == nil {
		t.Error("expected over-length key to be invalid")
	}
}

func TestNodeAttributeNameRoundtrip(t *testing.T) {
	node := uuid.New()
	other := uuid.New()
	name := NodeAttributeName(node, "ci_agent_token")

	if key, ok := ParseNodeAttributeKey(name, node); !ok || key != "ci_agent_token" {
		t.Fatalf("roundtrip failed: key=%q ok=%v", key, ok)
	}
	// The same backing name must NOT parse as an attribute of a different node.
	if _, ok := ParseNodeAttributeKey(name, other); ok {
		t.Fatal("attribute name leaked across nodes")
	}
	// A plain secret name is not an attribute.
	if _, ok := ParseNodeAttributeKey("regular-secret", node); ok {
		t.Fatal("plain name parsed as attribute")
	}
	if !IsNodeAttributeName(name) || IsNodeAttributeName("regular-secret") {
		t.Fatal("IsNodeAttributeName misclassified")
	}
}

func TestNodeAttributesFromSecrets(t *testing.T) {
	node := uuid.New()
	other := uuid.New()
	secrets := []Secret{
		{ID: uuid.New(), Name: NodeAttributeName(node, "token"), Value: "A", Sensitive: true},
		{ID: uuid.New(), Name: NodeAttributeName(node, "host"), Value: "10.0.0.1", Sensitive: false},
		{ID: uuid.New(), Name: NodeAttributeName(other, "token"), Value: "B"}, // other node
		{ID: uuid.New(), Name: "shared-secret", Value: "C"},                   // regular secret
	}
	attrs := NodeAttributesFromSecrets(node, secrets)
	if len(attrs) != 2 {
		t.Fatalf("expected 2 attributes for the node, got %d", len(attrs))
	}
	byKey := map[string]NodeAttribute{}
	for _, a := range attrs {
		byKey[a.Key] = a
	}
	if byKey["token"].Value != "A" || !byKey["token"].Sensitive {
		t.Errorf("unexpected token attribute: %+v", byKey["token"])
	}
	if byKey["host"].Value != "10.0.0.1" || byKey["host"].Sensitive {
		t.Errorf("unexpected host attribute: %+v", byKey["host"])
	}
}
