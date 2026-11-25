package workflow_execution

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/google/uuid"
)

// WorkflowExecutionStatus represents the overall status of a workflow execution
type WorkflowExecutionStatus string

const (
	StatusPending         WorkflowExecutionStatus = "pending"          // Created but not started
	StatusRunning         WorkflowExecutionStatus = "running"          // At least one node is executing
	StatusCompleted       WorkflowExecutionStatus = "completed"        // All nodes completed successfully
	StatusFailed          WorkflowExecutionStatus = "failed"           // All nodes failed
	StatusPartiallyFailed WorkflowExecutionStatus = "partially_failed" // Some nodes succeeded, some failed
)

// String returns the string representation of the status
func (s WorkflowExecutionStatus) String() string {
	return string(s)
}

// WorkflowExecution represents an execution instance of a workflow template
type WorkflowExecution struct {
	ID          uuid.UUID
	TemplateID  uuid.UUID
	AccountID   uuid.UUID
	Status      WorkflowExecutionStatus
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	NodeCount   int // Number of nodes in this execution (cached to avoid joins)
	// TemplateSnapshot is the template definition frozen at start time; the
	// engine runs the snapshot, so editing a template cannot affect
	// in-flight executions. Nil only for executions started before
	// snapshotting existed.
	TemplateSnapshot *workflow_template.TemplateSnapshot
	// OUID freezes the template's OU scope at start time, like the snapshot:
	// a run's visibility cannot change after launch. Nil = global playbook or
	// pre-migration row (visible to account-wide roles only).
	OUID *uuid.UUID
	// StartedBy / StartedKind attribute the run to a member or the scheduler.
	StartedBy   *uuid.UUID
	StartedKind StartedKind
	// NodeIDs are the execution's target nodes; derived from the execution
	// node rows for list/detail views, not persisted on this table.
	NodeIDs []uuid.UUID
}

// StartedKind distinguishes who started an execution.
type StartedKind string

const (
	StartedByUser      StartedKind = "user"
	StartedByScheduler StartedKind = "scheduler"
)

// NewWorkflowExecution creates a new workflow execution
func NewWorkflowExecution(templateID, accountID uuid.UUID) *WorkflowExecution {
	now := time.Now()
	return &WorkflowExecution{
		ID:          uuid.New(),
		TemplateID:  templateID,
		AccountID:   accountID,
		Status:      StatusPending,
		StartedAt:   nil,
		CompletedAt: nil,
		CreatedAt:   now,
		UpdatedAt:   now,
		NodeCount:   0, // Will be set when nodes are created
	}
}

// Start marks the execution as started
func (we *WorkflowExecution) Start() {
	now := time.Now()
	we.Status = StatusRunning
	we.StartedAt = &now
	we.UpdatedAt = now
}

// Complete marks the execution as completed
func (we *WorkflowExecution) Complete(status WorkflowExecutionStatus) {
	now := time.Now()
	we.Status = status
	we.CompletedAt = &now
	we.UpdatedAt = now
}
