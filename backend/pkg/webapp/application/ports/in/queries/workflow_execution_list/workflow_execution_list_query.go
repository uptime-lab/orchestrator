package workflow_execution_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// Pagination bounds for the executions list.
const (
	DefaultPerPage = 50
	MaxPerPage     = 200
)

// WorkflowExecutionListQuery lists workflow executions with optional filtering
// and pagination (newest first).
type WorkflowExecutionListQuery struct {
	AuthContext commands.AuthContext
	TemplateID  *uuid.UUID // Optional template filter
	// OUID keeps only runs targeting at least one node in this OU's subtree
	// (the OU itself + its direct children — the 2-level OU tree).
	OUID    *uuid.UUID
	Page    int // 1-based
	PerPage int
}

// Validate normalizes pagination.
func (q *WorkflowExecutionListQuery) Validate() error {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage <= 0 {
		q.PerPage = DefaultPerPage
	}
	if q.PerPage > MaxPerPage {
		q.PerPage = MaxPerPage
	}
	return nil
}
