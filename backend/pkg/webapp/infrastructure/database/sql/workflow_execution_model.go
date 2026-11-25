package sql

import (
	"encoding/json"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/google/uuid"
)

// WorkflowExecutionModel is the SQL representation of a workflow execution
type WorkflowExecutionModel struct {
	ID         uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	TemplateID uuid.UUID `gorm:"column:template_id;type:uuid;index;not null"`
	// (account_id, created_at) serves the paginated newest-first list; the
	// completed_at index serves the retention sweep and the active-count cap.
	AccountID        uuid.UUID  `gorm:"column:account_id;type:uuid;index;index:idx_workflow_executions_account_created,priority:1;not null"`
	Status           string     `gorm:"column:status;size:50;not null;default:'pending'"`
	StartedAt        *time.Time `gorm:"column:started_at;type:timestamp"`
	CompletedAt      *time.Time `gorm:"column:completed_at;type:timestamp;index"`
	NodeCount        int        `gorm:"column:node_count;not null;default:0"`
	TemplateSnapshot []byte     `gorm:"column:template_snapshot;type:jsonb"` // TemplateSnapshot JSON; null for pre-snapshot executions
	// OUID freezes the template's OU scope at start time (null for global
	// playbooks and pre-migration rows → visible to account-wide roles only).
	OUID *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`
	// StartedByUserID/StartedByKind attribute the run to a member or the
	// scheduler (audit; null/empty for pre-migration rows).
	StartedByUserID *uuid.UUID `gorm:"column:started_by_user_id;type:uuid"`
	StartedByKind   string     `gorm:"column:started_by_kind;size:20"`
	CreatedAt       time.Time  `gorm:"column:created_at;index:idx_workflow_executions_account_created,priority:2;not null;default:now()"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (WorkflowExecutionModel) TableName() string {
	return "workflow_executions"
}

// ToDomain converts the SQL model to a domain model
func (m *WorkflowExecutionModel) ToDomain() *workflow_execution.WorkflowExecution {
	domain := &workflow_execution.WorkflowExecution{
		ID:          m.ID,
		TemplateID:  m.TemplateID,
		AccountID:   m.AccountID,
		Status:      workflow_execution.WorkflowExecutionStatus(m.Status),
		StartedAt:   m.StartedAt,
		CompletedAt: m.CompletedAt,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		NodeCount:   m.NodeCount,
		OUID:        m.OUID,
		StartedBy:   m.StartedByUserID,
		StartedKind: workflow_execution.StartedKind(m.StartedByKind),
	}

	if len(m.TemplateSnapshot) > 0 {
		var snapshot workflow_template.TemplateSnapshot
		if err := json.Unmarshal(m.TemplateSnapshot, &snapshot); err == nil {
			domain.TemplateSnapshot = &snapshot
		}
	}

	return domain
}

// FromDomain updates the SQL model from a domain model
func (m *WorkflowExecutionModel) FromDomain(execution *workflow_execution.WorkflowExecution) {
	m.ID = execution.ID
	m.TemplateID = execution.TemplateID
	m.AccountID = execution.AccountID
	m.Status = string(execution.Status)
	m.StartedAt = execution.StartedAt
	m.CompletedAt = execution.CompletedAt
	m.NodeCount = execution.NodeCount
	m.OUID = execution.OUID
	m.StartedByUserID = execution.StartedBy
	m.StartedByKind = string(execution.StartedKind)
	m.CreatedAt = execution.CreatedAt
	m.UpdatedAt = execution.UpdatedAt

	m.TemplateSnapshot = nil
	if execution.TemplateSnapshot != nil {
		if snapshotJSON, err := json.Marshal(execution.TemplateSnapshot); err == nil {
			m.TemplateSnapshot = snapshotJSON
		}
	}
}
