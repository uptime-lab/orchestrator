package sql

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateModel is the SQL representation of a workflow template
type WorkflowTemplateModel struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID   uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	Name        string     `gorm:"column:name;size:255;not null"`
	Description string     `gorm:"column:description;type:text"`
	IsShortcut  bool       `gorm:"column:is_shortcut;default:false;not null"`
	// OUID is the LEGACY single-unit column: backfilled once into
	// workflow_template_ous and never read again. Kept so a rollback to an
	// older binary still finds a scope, exactly as access_entries.ou_id is.
	OUID *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`
	Status      string     `gorm:"column:status;size:20;not null;default:'active'"`
	Steps       []byte     `gorm:"column:steps;type:jsonb;not null"` // JSON array of WorkflowStep
	Graph       []byte     `gorm:"column:graph;type:jsonb"`          // WorkflowGraph JSON; null for legacy templates
	// ScheduleCron denormalizes the graph's schedule-trigger cron ("" = manual
	// only) so the per-minute scheduler scan filters in SQL instead of loading
	// every template. Maintained on write from the graph; NULL only on rows
	// predating the column (backfilled in Setup).
	ScheduleCron string `gorm:"column:schedule_cron;size:120;index:idx_workflow_templates_schedule,where:schedule_cron <> ''"`
	// AuthorID is the user who last passed the authoring gate, re-checked by
	// the cron scheduler before firing a privileged playbook. NULL on rows
	// predating the column.
	AuthorID  *uuid.UUID `gorm:"column:author_id;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (WorkflowTemplateModel) TableName() string {
	return "workflow_templates"
}

// WorkflowTemplateOUModel is the join row scoping a playbook to a unit. One
// playbook may reference several units; the composite primary key makes
// re-assignment idempotent.
type WorkflowTemplateOUModel struct {
	TemplateID uuid.UUID `gorm:"column:template_id;type:uuid;primaryKey;not null"`
	OUID       uuid.UUID `gorm:"column:ou_id;type:uuid;primaryKey;not null;index"`
	AccountID  uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
}

func (WorkflowTemplateOUModel) TableName() string { return "workflow_template_ous" }

// ToDomain converts the SQL model to a domain model.
//
// It returns an error rather than substituting an empty document for one it
// could not parse. Both jsonb columns ARE the playbook: an unreadable `steps`
// used to become `[]` and an unreadable `graph` became nil, so a corrupted row
// read back as a valid playbook that does nothing — and, through
// EffectiveGraph, as a bare trigger chain that has silently lost its schedule.
// A read that fails is recoverable; one that succeeds with the wrong document
// is not.
func (m *WorkflowTemplateModel) ToDomain() (*workflow_template.WorkflowTemplate, error) {
	domain := &workflow_template.WorkflowTemplate{
		ID:          m.ID,
		AccountID:   m.AccountID,
		Name:        m.Name,
		Description: m.Description,
		IsShortcut:  m.IsShortcut,
		Status:      workflow_template.WorkflowTemplateStatus(m.Status),
		AuthorID:    m.AuthorID,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}

	// Parse Steps from JSON
	domain.Steps = []workflow_template.WorkflowStep{}
	if len(m.Steps) > 0 {
		var steps []workflow_template.WorkflowStep
		if err := json.Unmarshal(m.Steps, &steps); err != nil {
			return nil, fmt.Errorf("playbook %s has unreadable steps: %w", m.ID, err)
		}
		domain.Steps = steps
	}

	// Parse Graph from JSON (null for legacy templates)
	if len(m.Graph) > 0 {
		var graph workflow_template.WorkflowGraph
		if err := json.Unmarshal(m.Graph, &graph); err != nil {
			return nil, fmt.Errorf("playbook %s has an unreadable graph: %w", m.ID, err)
		}
		domain.Graph = &graph
	}

	return domain, nil
}

// FromDomain updates the SQL model from a domain model. A marshal failure is
// returned rather than swallowed: writing "[]" over a real step list, or
// leaving the graph column null because it would not serialize, destroys the
// playbook at exactly the moment the caller believes it saved one.
func (m *WorkflowTemplateModel) FromDomain(template *workflow_template.WorkflowTemplate) error {
	m.ID = template.ID
	m.AccountID = template.AccountID
	m.Name = template.Name
	m.Description = template.Description
	m.IsShortcut = template.IsShortcut
	// The legacy column keeps a single representative unit so an older binary
	// reading this row still sees a scope rather than "all nodes"; the join
	// table is the truth.
	m.OUID = template.PrimaryOUID()
	if m.OUID == nil && len(template.OUIDs) > 0 {
		first := template.OUIDs[0]
		m.OUID = &first
	}
	m.Status = string(template.Status)
	m.AuthorID = template.AuthorID
	m.CreatedAt = template.CreatedAt
	m.UpdatedAt = template.UpdatedAt

	// Serialize Steps to JSON
	m.Steps = []byte("[]")
	if len(template.Steps) > 0 {
		stepsJSON, err := json.Marshal(template.Steps)
		if err != nil {
			return fmt.Errorf("serializing playbook %s steps: %w", template.ID, err)
		}
		m.Steps = stepsJSON
	}

	// Serialize Graph to JSON (null for legacy templates)
	m.Graph = nil
	if template.Graph != nil {
		graphJSON, err := json.Marshal(template.Graph)
		if err != nil {
			return fmt.Errorf("serializing playbook %s graph: %w", template.ID, err)
		}
		m.Graph = graphJSON
	}

	// Keep the denormalized scheduler column in sync with the graph.
	m.ScheduleCron = ""
	if g := template.EffectiveGraph(); g != nil {
		m.ScheduleCron = g.ScheduleCron()
	}
	return nil
}
