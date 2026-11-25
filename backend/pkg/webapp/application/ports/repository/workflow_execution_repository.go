package repository

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/google/uuid"
)

// WorkflowExecutionListFilter narrows and pages the executions list. Rows are
// always ordered created_at DESC (newest first) with template_snapshot omitted.
type WorkflowExecutionListFilter struct {
	// TemplateID limits the list to one playbook's runs.
	TemplateID *uuid.UUID
	// NodeOUIDs, when non-empty, keeps only executions that target at least one
	// node currently assigned to one of these OUs (the caller expands the
	// subtree). Mirrors the Runs page OU rail semantics.
	NodeOUIDs []uuid.UUID
	// Offset/Limit page the result; Limit <= 0 means no page bound.
	Offset int
	Limit  int
}

// WorkflowExecutionRepository defines the interface for workflow execution database operations
type WorkflowExecutionRepository interface {
	// Create creates a new workflow execution
	Create(ctx context.Context, execution *workflow_execution.WorkflowExecution) error

	// Update writes every column of an existing workflow execution. It refuses
	// with app_errors.ErrExecutionAlreadyCompleted when the stored run already
	// carries completed_at, so a copy read before a concurrent settle cannot
	// reopen a finished run.
	Update(ctx context.Context, execution *workflow_execution.WorkflowExecution) error

	// MarkStarted moves a PENDING run to running and stamps started_at, writing
	// those two columns and nothing else. It returns the stored row as it is
	// after the write, so the caller can adopt a copy that includes whatever
	// landed underneath it — node_count above all.
	//
	// The engine cannot use Update here: its copy of the execution was read by a
	// worker that the creation loop enqueued BEFORE the count was known, so a
	// full-row write put node_count back to 0 and a 50-node run showed "0 nodes"
	// for the rest of its life.
	MarkStarted(ctx context.Context, accountID, executionID uuid.UUID, startedAt time.Time) (*workflow_execution.WorkflowExecution, error)

	// UpdateNodeCount writes node_count alone. For callers whose copy of the
	// execution may already be stale — the workers are started before the count
	// is known — so a full-row write would revert whatever they wrote.
	UpdateNodeCount(ctx context.Context, accountID, executionID uuid.UUID, nodeCount int) error

	// GetByID retrieves a workflow execution by ID within an account
	GetByID(ctx context.Context, accountID, executionID uuid.UUID) (*workflow_execution.WorkflowExecution, error)

	// List returns one page of the account's executions (newest first,
	// template_snapshot omitted) plus the total row count for the filter.
	List(ctx context.Context, accountID uuid.UUID, filter WorkflowExecutionListFilter) ([]workflow_execution.WorkflowExecution, int64, error)

	// CountActiveByAccountID counts the account's pending+running executions
	// (the in-flight set the per-account cap is enforced against).
	CountActiveByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)

	// ListExpiredIDs returns up to limit ids of terminal-status executions
	// created before cutoff (a retention batch), oldest first.
	ListExpiredIDs(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error)

	// DeleteByIDs deletes executions by id and reports how many rows went.
	DeleteByIDs(ctx context.Context, ids []uuid.UUID) (int64, error)
}
