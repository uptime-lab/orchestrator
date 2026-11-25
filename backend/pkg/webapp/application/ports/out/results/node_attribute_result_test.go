package results

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

func TestNewNodeAttributeResultGatesValue(t *testing.T) {
	sensitive := NewNodeAttributeResult(secret.NodeAttribute{
		SecretID: uuid.New(), Key: "token", Value: "s3cr3t", Sensitive: true,
	})
	if sensitive.Value != nil {
		t.Errorf("sensitive attribute must not expose its value, got %v", *sensitive.Value)
	}
	if !sensitive.HasValue {
		t.Error("expected has_value true for a sensitive attribute with a value")
	}

	variable := NewNodeAttributeResult(secret.NodeAttribute{
		SecretID: uuid.New(), Key: "host", Value: "10.0.0.1", Sensitive: false,
	})
	if variable.Value == nil || *variable.Value != "10.0.0.1" {
		t.Errorf("non-sensitive attribute must expose its value, got %v", variable.Value)
	}
}

func TestNewNodeAttributeListResult(t *testing.T) {
	list := NewNodeAttributeListResult([]secret.NodeAttribute{
		{SecretID: uuid.New(), Key: "a", Value: "1", Sensitive: false},
		{SecretID: uuid.New(), Key: "b", Value: "2", Sensitive: true},
	})
	if list.Count != 2 || len(list.Attributes) != 2 {
		t.Fatalf("expected 2 attributes, got %d", list.Count)
	}
}
