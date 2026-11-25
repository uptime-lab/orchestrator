package results

import (
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// NodeActivityResult is one dispatched device command as the fleet activity
// log shows it.
//
// It carries NO payload and NO result, and that is a decision, not an
// omission. Serving those resolves the caller's secret.read_value permission,
// and resolving it writes a `secret.values_read` audit row — so a paged log
// would stamp the trail once per page load with a read nobody performed. The
// row links to GET /api/v1/node-commands/{id}, which does that properly, once,
// for the one command an operator actually opened.
type NodeActivityResult struct {
	ID       uuid.UUID  `json:"id"`
	NodeID   uuid.UUID  `json:"node_id"`
	NodeName string     `json:"node_name,omitempty"`
	OUID     *uuid.UUID `json:"ou_id,omitempty"`
	Slug     string     `json:"slug"`
	Status   string     `json:"status"`
	Progress int        `json:"progress,omitempty"`
	// Queueable marks a command that waits for an offline node. A queued
	// command counts as dispatched and has NOT been applied.
	Queueable bool `json:"queueable,omitempty"`
	// Details is the agent's free text, redacted where the command carried
	// secret material. It is the only triage text this list shows.
	Details  string `json:"details,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	// The owning run is deliberately absent. workflow_execution_node_id is a
	// column the domain command does not carry, so naming it here would cost a
	// query per row; run history has its own view under Operations.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NodeActivityListResult is one page of the fleet activity log.
type NodeActivityListResult struct {
	Activity []NodeActivityResult `json:"activity"`
	Count    int                  `json:"count"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PerPage  int                  `json:"per_page"`
}

// NewNodeActivityResult converts one command. nodeName and ouID come from the
// caller's already-loaded node set, so the log does not re-read a node per row.
func NewNodeActivityResult(cmd *node_command.NodeCommand, n *node.Node) NodeActivityResult {
	out := NodeActivityResult{
		ID:        cmd.ID,
		NodeID:    cmd.NodeID,
		Slug:      cmd.Slug,
		Status:    string(cmd.Status),
		Progress:  cmd.Progress,
		Queueable: cmd.Queueable,
		// allowSecretValues is false and stays false here — see the type doc.
		Details:   PublicCommandDetails(cmd.Details, cmd.Payload, false),
		Attempts:  cmd.Attempts,
		CreatedAt: cmd.CreatedAt,
		UpdatedAt: cmd.UpdatedAt,
	}
	if n != nil {
		out.NodeName = n.Name
		out.OUID = n.OUID
	}
	return out
}

// NewNodeActivityListResult wraps a page. Page and perPage are the EFFECTIVE
// values after normalization, so a client's paging math is not fed back its
// own out-of-range input.
func NewNodeActivityListResult(activity []NodeActivityResult, total int64, page, perPage int) *NodeActivityListResult {
	if activity == nil {
		activity = []NodeActivityResult{}
	}
	return &NodeActivityListResult{
		Activity: activity,
		Count:    len(activity),
		Total:    total,
		Page:     page,
		PerPage:  perPage,
	}
}
