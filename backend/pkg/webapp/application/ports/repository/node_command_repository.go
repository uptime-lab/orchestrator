package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// NodeCommandRepository defines the interface for node command repository operations
type NodeCommandRepository interface {
	Create(ctx context.Context, nodeCommand *node_command.NodeCommand) error
	CreateWithWorkflowNode(ctx context.Context, nodeCommand *node_command.NodeCommand, workflowExecutionNodeID *uuid.UUID) error                            // Create with optional workflow_execution_node_id
	GetByID(ctx context.Context, accountID, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error)                                                     // for HTTP queries (with account validation)
	GetByIDForUpdate(ctx context.Context, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error)                                                       // for gRPC status updates (no account check)
	GetRegisteredByNodeID(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error)                                             // get registered commands for a node (called by gRPC server)
	GetQueueableRegisteredByNodeID(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error)                                    // queued commands awaiting an offline node, oldest first (reconnect redelivery)
	ResetSentQueueableToRegistered(ctx context.Context, nodeID uuid.UUID) (int64, error)                                                                    // return delivered-but-unacknowledged queueable commands to pending so redelivery picks them up
	SupersedeRegisteredByKey(ctx context.Context, nodeID uuid.UUID, supersedeKey string, excludeID uuid.UUID, createdBefore time.Time) ([]uuid.UUID, error) // supersede unsent commands for the same key that are OLDER than createdBefore; returns the ids so the caller can settle each
	IncrementAttempts(ctx context.Context, nodeCommandID uuid.UUID) error                                                                                   // count a delivery try
	GetExpiredQueuedCommands(ctx context.Context, now time.Time, limit int) ([]node_command.NodeCommand, error)
	// GetAbandonedInProgressCommands returns in_progress commands, across every
	// account, that have not reported since staleBefore. The backstop for a
	// terminal report that never arrived on a node that is still connected —
	// the two existing sweeps are both triggered by the node going away.
	GetAbandonedInProgressCommands(ctx context.Context, staleBefore time.Time, limit int) ([]node_command.NodeCommand, error) // queued commands past their TTL (sweeper for nodes that never reconnect)
	// GetUnsettledCommandsOfOfflineNodes returns the non-queueable commands
	// still owed an outcome on nodes that are OFFLINE, with both the command
	// and the node itself silent since staleBefore, device-exclusive slugs
	// included. The connected-node
	// backstop above excludes offline nodes; this is their half.
	GetUnsettledCommandsOfOfflineNodes(ctx context.Context, staleBefore time.Time, limit int) ([]node_command.NodeCommand, error)
	// GetUnsettledCommandsByNodeID returns every non-terminal command of one
	// node (registered, sent, in_progress), for settling them at node delete.
	GetUnsettledCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error)
	GetByNodeID(ctx context.Context, accountID, nodeID uuid.UUID, limit, offset int) ([]node_command.NodeCommand, error)      // paginated list for HTTP queries
	// ListActivity is the account-wide, time-ordered read behind the fleet
	// activity log. It returns one page plus the total matching the filter.
	ListActivity(ctx context.Context, accountID uuid.UUID, filter NodeCommandActivityFilter, offset, limit int) ([]node_command.NodeCommand, int64, error)
	// BackfillBootloaderFlashedAt seeds nodes.bootloader_flashed_at from the
	// command log — the derivation the attention check used to run per
	// request — so state recorded before the column existed survives the
	// switch to the settle-hook stamp. Idempotent: it only fills NULLs.
	BackfillBootloaderFlashedAt() error
	// NodesWithActiveCommands returns, per node, the command the platform has
	// not settled yet — registered, sent or in progress. One grouped query, so
	// a fleet-wide view can both withhold an action from a node already being
	// worked on and say WHAT is running: "busy" alone leaves an operator
	// watching an eight-minute package install with no sign it is progressing.
	NodesWithActiveCommands(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]ActiveCommand, error)
	// LatestAttempts returns, per node and per slug, the account's most recent
	// command with that slug: its id, how it ended and when.
	//
	// A PROJECTION on purpose. The useful part of a failure is the agent's own
	// sentence, and that lives in the encrypted details column behind a
	// redaction gate that needs the payload — so this carries the id instead
	// and the caller fetches the one command an operator actually opened.
	// Hydrating every node's latest command to show a message nobody has asked
	// for would decrypt the whole fleet's payloads on a page load.
	LatestAttempts(ctx context.Context, accountID uuid.UUID, slugs []string) (map[uuid.UUID]map[string]CommandAttempt, error)
	GetByWorkflowExecutionNodeID(ctx context.Context, workflowExecutionNodeID uuid.UUID) ([]node_command.NodeCommand, error) // get commands for a workflow execution node
	GetWorkflowExecutionNodeID(ctx context.Context, nodeCommandID uuid.UUID) (*uuid.UUID, error)                             // get workflow_execution_node_id for a command (returns nil if not part of workflow)
	// GetWorkflowLink returns the command's owning account and node plus its
	// workflow link, in one unlocked projection. The workflow engine's
	// completion hook runs for EVERY device command, most of which belong to no
	// run at all, so that path must not read (nor FOR UPDATE) the whole row.
	GetWorkflowLink(ctx context.Context, nodeCommandID uuid.UUID) (*node_command.WorkflowLink, error)
	UpdateWorkflowExecutionNodeID(ctx context.Context, nodeCommandID uuid.UUID, workflowExecutionNodeID *uuid.UUID) error // update workflow_execution_node_id for a command
	Update(ctx context.Context, nodeCommand *node_command.NodeCommand) error
	UpdateStatus(ctx context.Context, nodeCommandID uuid.UUID, status node_command.NodeCommandStatus, progress int, details string) error                                    // optimized update for status changes
	SetResult(ctx context.Context, nodeCommandID uuid.UUID, result map[string]string) error                                                                                  // structured agent outputs (workflow blocks)
	UpdateStatusIfCurrent(ctx context.Context, nodeCommandID uuid.UUID, currentStatus, newStatus node_command.NodeCommandStatus, progress int, details string) (bool, error) // updates status only if current status matches currentStatus. Returns (updated bool, error). Uses database-level conditional update (e.g., WHERE id = ? AND status = ?) to ensure atomicity. Useful for conditional updates like MarkNodeCommandSent
	HasActiveCommands(ctx context.Context, nodeID uuid.UUID) (bool, error)
	// FindActiveDeviceCommand returns the oldest unfinished command for this node
	// among slugs that own a block device, or nil. Distinct from
	// HasActiveCommands, which asks whether the NODE is busy: a queued command
	// leaves the node idle but still spoken for.
	FindActiveDeviceCommand(ctx context.Context, nodeID uuid.UUID, slugs []string) (*node_command.NodeCommand, error)                                            // checks if node has any commands with status registered, sent, or in_progress
	GetInProgressRebootCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error)                                               // returns all commands with status "in_progress" and slug "reboot" for a given node
	GetStalePendingNonRebootCommandsByNodeID(ctx context.Context, nodeID uuid.UUID, continuity node.AgentInstanceContinuity) ([]node_command.NodeCommand, error) // non-reboot commands abandoned by a previous agent session. The in_progress arm follows the continuity verdict: Same = skip (the process still executes them), Changed = all of them (that work is certainly lost), Unknown = the grace-period heuristic
	GetStalePendingCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error)                                                   // same, INCLUDING reboot: for a node the monitor has concluded is not coming back, its reboot did not complete either
	DeleteByWorkflowExecutionNodeIDs(ctx context.Context, workflowExecutionNodeIDs []uuid.UUID) (int64, error)                                                   // retention: sweep workflow-issued command history with its executions
	// DeleteTerminalOlderThan removes commands that have REACHED a terminal
	// status and are older than cutoff, at most limit per call.
	//
	// Terminal-only, and enforced in the query rather than by the caller: a
	// `registered` command is not old history. Queueable slugs sit registered
	// for as long as their node is offline — which can be months — and deleting
	// one by age would silently drop work the platform promised to deliver on
	// reconnect.
	DeleteTerminalOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	// CountActiveByAccountAndSlug counts one account's commands of a slug that
	// have a device WORKING right now (sent / in_progress) — the utility
	// fleet-pacing bound. `registered` (queued for an offline node, up to its
	// 24 h TTL) does not count: see the implementation's comment.
	CountActiveByAccountAndSlug(ctx context.Context, accountID uuid.UUID, slug string) (int64, error)
	// CountLiveByNodeAndSlug counts one node's commands of a slug that are
	// not yet terminal, registered (queued) INCLUDED — the reconcile loop's
	// "does anything already own this node's convergence" question.
	CountLiveByNodeAndSlug(ctx context.Context, accountID, nodeID uuid.UUID, slug string) (int64, error)
	// LatestSucceededSlugByNode returns the slug of one node's most recent
	// command, among the given slugs, that ended in success; "" when there is
	// none on record. Terminal rows are pruned by retention, so "" means
	// "none on record", not "never happened".
	LatestSucceededSlugByNode(ctx context.Context, accountID, nodeID uuid.UUID, slugs []string) (string, error)
}

// NodeCommandActivityFilter narrows the fleet activity log. Every field is
// optional; a zero filter is "everything this account has ever dispatched".
//
// NodeIDs is not a user filter — it carries the caller's authz visibility, and
// an EMPTY, non-nil slice therefore means "this member may see no node" and
// must return nothing. A nil slice means no narrowing. Collapsing the two into
// one nil check would show an OU-scoped member the whole account.
type NodeCommandActivityFilter struct {
	NodeIDs  []uuid.UUID
	Statuses []string
	Slug     string
	Since    *time.Time
	Until    *time.Time
}

// CommandAttempt is how one node's most recent command with a given slug
// ended. Enough to say "the last try failed, at this time, and here is the row
// to open" without decrypting anything.
type CommandAttempt struct {
	CommandID uuid.UUID
	Status    string
	At        time.Time
}

// ActiveCommand is the unsettled command a node is carrying: enough to name it
// and show how far along it says it is.
type ActiveCommand struct {
	Slug     string
	Progress int
	// UpdatedAt is the last time the node said anything about it. A command
	// that has not moved for a long while is the shape a LOST terminal report
	// leaves behind, and nothing else notices it: the abandonment sweep runs
	// on reconnect, so a command orphaned by an agent that reconnected in the
	// same second stays in_progress for as long as that agent keeps
	// heartbeating.
	UpdatedAt time.Time
}
