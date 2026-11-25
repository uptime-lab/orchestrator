package node_command

import (
	"fmt"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// MaxBatchNodes caps one batch dispatch. Without a cap an accidental
// "select all" turns into a fleet-wide change with no chance to reconsider;
// callers that genuinely mean it can page through deliberately.
const MaxBatchNodes = 500

// BatchNodeCommandCommand represents a command to send a command to multiple nodes
type BatchNodeCommandCommand struct {
	NodeIDs                 []uuid.UUID            `json:"node_ids"`         // List of node IDs to send command to
	Command                 string                 `json:"command"`          // "reboot", "shutdown", "status_check", etc.
	Params                  map[string]interface{} `json:"params,omitempty"` // Optional key:value params (e.g., {"delay_seconds": 0})
	AuthContext             commands.AuthContext   `json:"-"`                // Set from context, not from body
	WorkflowExecutionNodeID *uuid.UUID             `json:"-"`                // Optional workflow execution node ID (for workflow commands)
	// SecretDerivedParams names the params the workflow engine resolved
	// {{secrets.*}} / {{node.attributes.*}} material into, so the API can redact
	// exactly those. Engine-internal: it is derived from the UNRESOLVED template
	// and cannot be recovered downstream, and it is never read from a request
	// body.
	SecretDerivedParams []string `json:"-"`
	// WorkflowBlockID names the graph block this dispatch executes, recorded
	// into the payload under results.WorkflowBlockIDKey so a pass lost between
	// dispatch and finalize can be reconciled instead of re-dispatched.
	// Engine-only, like WorkflowExecutionNodeID.
	WorkflowBlockID string `json:"-"`
}

// Validate bounds the batch and normalizes the target list. It must run before
// anything that costs one operation PER SUPPLIED ID — the HTTP handler
// authorizes each target with its own node query, so a cap checked further down
// the call chain is paid for once per element the caller sent rather than once
// per node the batch can actually reach.
func (c *BatchNodeCommandCommand) Validate() error {
	if c.Command == "" {
		return fmt.Errorf("command is required")
	}
	if len(c.NodeIDs) == 0 {
		return fmt.Errorf("at least one node ID is required")
	}
	// Counted on the list as SUPPLIED, before deduplication: a caller that sends
	// half a million copies of one id is over the limit it was told about.
	if len(c.NodeIDs) > MaxBatchNodes {
		return fmt.Errorf("too many nodes in one batch: %d (limit %d)", len(c.NodeIDs), MaxBatchNodes)
	}
	c.NodeIDs = dedupeNodeIDs(c.NodeIDs)
	return nil
}

// dedupeNodeIDs drops repeated ids, preserving order. A repeat is not an error —
// it is a client sending the same node twice — but dispatching to it twice is,
// and every repeat would otherwise land in the batch result as its own item.
func dedupeNodeIDs(in []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(in))
	out := make([]uuid.UUID, 0, len(in))
	for _, id := range in {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
