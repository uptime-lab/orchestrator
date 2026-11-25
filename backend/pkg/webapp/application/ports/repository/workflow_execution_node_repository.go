package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/google/uuid"
)

// WorkflowExecutionNodeRepository defines the interface for workflow execution node database operations
type WorkflowExecutionNodeRepository interface {
	// Create creates a new workflow execution node
	Create(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) error

	// Update updates an existing workflow execution node
	Update(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) error

	// UpdateActive persists the node ONLY while the stored row is still
	// pending/running, reporting whether it wrote. The engine uses it so a worker
	// holding a pre-cancel copy cannot resurrect a run that CancelExecution or
	// AbandonNodeExecutions settled underneath it.
	UpdateActive(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) (bool, error)

	// GetByID retrieves a workflow execution node by ID
	GetByID(ctx context.Context, executionNodeID uuid.UUID) (*workflow_execution_node.WorkflowExecutionNode, error)

	// GetByExecutionID retrieves all workflow execution nodes for a specific execution
	GetByExecutionID(ctx context.Context, executionID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error)

	// GetActiveByNodeID retrieves the PENDING/RUNNING execution nodes of a
	// specific node.
	//
	// There is deliberately no getter for "every execution node this node ever
	// had": both callers (settling a deleted node's runs, and the completion
	// lookup below) only ever want unfinished work, while the history of a node
	// that has run a per-minute playbook for a month is unbounded — and every row
	// costs a graph_state decryption to build.
	GetActiveByNodeID(ctx context.Context, accountID, nodeID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error)

	// FindIDByCurrentCommandID returns the id of the execution node currently
	// waiting on the given device command, or uuid.Nil when no run is.
	//
	// This is the fallback for a workflow command whose back-link was never
	// written, and it runs on EVERY device command completion — including every
	// manual reboot and scan, which are not part of any run. So it answers in one
	// indexed row rather than by loading the node's execution history and
	// scanning it in Go.
	FindIDByCurrentCommandID(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (uuid.UUID, error)
	// CountByExecutionID counts the number of nodes for a specific execution
	CountByExecutionID(ctx context.Context, executionID uuid.UUID) (int64, error)
	// GetNodeIDsByExecutionIDs returns the target node ids of each execution
	// in a single query (for list views).
	GetNodeIDsByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)

	// GetIDsByExecutionIDs returns the execution-node row ids belonging to the
	// given executions (retention uses them to sweep command history).
	GetIDsByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) ([]uuid.UUID, error)

	// GetActiveIDs returns the ids of all pending/running execution nodes
	// (startup re-enqueue: the work queue is in-memory and lost on restart).
	GetActiveIDs(ctx context.Context) ([]uuid.UUID, error)

	// CountActiveByAccountID counts an account's pending+running execution
	// nodes. This — not the execution count beside it — is what consumes the
	// process-global worker pool: 20 executions times an unbounded node count is
	// unbounded, and node rows are free.
	CountActiveByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)

	// DeleteByExecutionIDs deletes all execution nodes of the given executions.
	DeleteByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) (int64, error)
}
