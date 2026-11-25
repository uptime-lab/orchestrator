package results

import (
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// NodeIdentifyStateResult is one node's transient identify/button state.
type NodeIdentifyStateResult struct {
	NodeID     uuid.UUID  `json:"node_id"`
	BlinkUntil *time.Time `json:"blink_until,omitempty"`
	PressedAt  *time.Time `json:"pressed_at,omitempty"`
}

// NewNodeIdentifyStateResults converts the registry entries.
func NewNodeIdentifyStateResults(states []node.IdentifyState) []NodeIdentifyStateResult {
	out := make([]NodeIdentifyStateResult, 0, len(states))
	for _, st := range states {
		out = append(out, NodeIdentifyStateResult{
			NodeID:     st.NodeID,
			BlinkUntil: st.BlinkUntil,
			PressedAt:  st.PressedAt,
		})
	}
	return out
}
