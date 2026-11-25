package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// NodeAttributeResult is one AD-style custom attribute of a node. Value is
// included only for non-sensitive attributes; sensitive ones are write-only and
// report has_value instead.
type NodeAttributeResult struct {
	SecretID  uuid.UUID `json:"secret_id"`
	Key       string    `json:"key"`
	Sensitive bool      `json:"sensitive"`
	Value     *string   `json:"value,omitempty"`
	HasValue  bool      `json:"has_value"`
	// Unreadable: set, but not decryptable with any configured key.
	Unreadable bool `json:"unreadable,omitempty"`
}

// NewNodeAttributeResult maps a domain attribute to the DTO, gating value
// visibility on the sensitive flag.
func NewNodeAttributeResult(a secret.NodeAttribute) NodeAttributeResult {
	res := NodeAttributeResult{
		SecretID:   a.SecretID,
		Key:        a.Key,
		Sensitive:  a.Sensitive,
		HasValue:   a.Value != "" || a.Unreadable,
		Unreadable: a.Unreadable,
	}
	if !a.Sensitive {
		value := a.Value
		res.Value = &value
	}
	return res
}

// NodeAttributeListResult is a node's full custom attribute set.
type NodeAttributeListResult struct {
	Attributes []NodeAttributeResult `json:"attributes"`
	Count      int                   `json:"count"`
}

// NewNodeAttributeListResult maps a node's attributes to the list DTO.
func NewNodeAttributeListResult(attrs []secret.NodeAttribute) NodeAttributeListResult {
	items := make([]NodeAttributeResult, len(attrs))
	for i := range attrs {
		items[i] = NewNodeAttributeResult(attrs[i])
	}
	return NodeAttributeListResult{Attributes: items, Count: len(items)}
}

// RedactValues strips plaintext attribute values for callers lacking
// secret.read_value (per-node attribute tokens must not leak to viewers).
func (r *NodeAttributeListResult) RedactValues() {
	for i := range r.Attributes {
		r.Attributes[i].Value = nil
	}
}
