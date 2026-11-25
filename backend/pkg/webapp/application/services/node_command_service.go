package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// NodeCommandService defines the interface for node command operations
type NodeCommandService interface {
	RegisterNodeCommand(ctx context.Context, nodeID uuid.UUID, slug string, payload map[string]interface{}, authContext interface{}) (*node_command.NodeCommand, error)
	RegisterNodeCommandWithWorkflowNode(ctx context.Context, nodeID uuid.UUID, slug string, payload map[string]interface{}, authContext interface{}, workflowExecutionNodeID *uuid.UUID) (*node_command.NodeCommand, error)
	GetRegisteredNodeCommands(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error)
	MarkNodeCommandSent(ctx context.Context, nodeCommandID uuid.UUID) error
	MarkNodeCommandFailed(ctx context.Context, nodeCommandID uuid.UUID, errorDetails string) error
	// FailCommandOfStoppedRun fails the command a stopped playbook run was
	// waiting on at ANY non-terminal stage, in_progress included, and releases
	// the node as the other failure settles do. The device is not told.
	FailCommandOfStoppedRun(ctx context.Context, nodeCommandID uuid.UUID, reason string) error
	UpdateNodeCommandStatus(ctx context.Context, nodeCommandID uuid.UUID, status string, progress int, details string) error
	// UpdateNodeCommandStatusWithResult applies an agent's report and says
	// whether it was applied. False means the command was already terminal, so
	// there is no transition and a caller's side effects must not run.
	UpdateNodeCommandStatusWithResult(ctx context.Context, nodeCommandID uuid.UUID, status string, progress int, details string, result map[string]string) (bool, error)
	GetNodeCommandByID(ctx context.Context, accountID, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error)
	GetNodeCommandsByNodeID(ctx context.Context, accountID, nodeID uuid.UUID, limit, offset int) ([]node_command.NodeCommand, error)
	HasActiveCommands(ctx context.Context, nodeID uuid.UUID) (bool, error)
	// FindActiveDeviceCommand returns the unfinished command that currently owns
	// this node's disk, or nil. Separate from HasActiveCommands, which answers
	// whether the NODE is busy: a queued command leaves the node idle while its
	// target device is already spoken for.
	FindActiveDeviceCommand(ctx context.Context, nodeID uuid.UUID) (*node_command.NodeCommand, error)
	// LatestSucceededCommand returns the slug of the node's most recent
	// successful command among slugs, "" when none is on record (retention
	// prunes old terminal rows).
	LatestSucceededCommand(ctx context.Context, accountID, nodeID uuid.UUID, slugs []string) (string, error)
	CompleteRebootCommandsForNode(ctx context.Context, nodeID uuid.UUID) error
	FailStalePendingCommandsForNode(ctx context.Context, nodeID uuid.UUID, continuity node.AgentInstanceContinuity) error
	// FailCommandsForUnreachableNode settles the commands of a node that never
	// came back, INCLUDING its reboot. Returns how many were failed. Does not
	// touch the node's status: the caller has decided the node is unreachable
	// and owns that transition.
	FailCommandsForUnreachableNode(ctx context.Context, nodeID uuid.UUID) (int, error)

	// FailAbandonedInProgressCommands settles in_progress commands that have
	// gone silent, across every account, and returns how many. The third
	// sweep: the other two are triggered by a node going away, so a command
	// orphaned on a node that stays CONNECTED — a terminal report that never
	// arrived — was invisible to both.
	FailAbandonedInProgressCommands(ctx context.Context) (int, error)
	// SettleCommandsForDeletedNode settles every outstanding command of a node
	// being deleted — queued ones expired, delivered ones failed — except the
	// slugs named in keepSlugs, and returns how many it settled.
	SettleCommandsForDeletedNode(ctx context.Context, nodeID uuid.UUID, keepSlugs ...string) (int, error)
	// RedeliverQueuedCommandsForNode dispatches commands queued while the node
	// was offline. Must be called only after the connection is registered.
	// Commands that require a managed OS are deferred to
	// RedeliverManagedOSQueuedCommandsForNode.
	RedeliverQueuedCommandsForNode(ctx context.Context, nodeID uuid.UUID) error
	// RedeliverManagedOSQueuedCommandsForNode dispatches queued commands that
	// must run on a managed OS. Call when a fresh system-info report confirms
	// the node is not in the recovery bootstrap.
	RedeliverManagedOSQueuedCommandsForNode(ctx context.Context, nodeID uuid.UUID) error
}
