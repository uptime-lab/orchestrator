package workflow_template_create

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateCreateCommand represents a command to create a new workflow template
type WorkflowTemplateCreateCommand struct {
	ID          uuid.UUID                        `json:"-"` // Set internally, not from request
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	IsShortcut  bool                             `json:"is_shortcut"`
	Steps       []workflow_template.WorkflowStep `json:"steps"`
	// Graph is the node-graph definition; when provided it is the source of
	// truth and Steps are derived from it server-side.
	Graph *workflow_template.WorkflowGraph `json:"graph"`
	// OUID is the LEGACY single-unit field, kept so an existing client keeps
	// working; OUIDs supersedes it. Read them through ScopeOUIDs, never
	// directly — a caller that sent only one and a caller that sent a set must
	// end up in the same place.
	OUID *uuid.UUID `json:"ou_id"`
	// OUIDs scopes the playbook to a SET of organizational units; empty = all
	// nodes of the account.
	OUIDs []uuid.UUID `json:"ou_ids"`
	// Status is active or draft; empty keeps the default/current value
	Status      string               `json:"status"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}

// ScopeOUIDs is the scope the caller asked for, from either field.
//
// A client sending the legacy single `ou_id` and one sending `ou_ids` must land
// in the same place, and `ou_ids` wins when both are present: it is the field
// that can express "no units" as well as "these units", which `ou_id` cannot.
func (c WorkflowTemplateCreateCommand) ScopeOUIDs() []uuid.UUID {
	if c.OUIDs != nil {
		return c.OUIDs
	}
	if c.OUID != nil {
		return []uuid.UUID{*c.OUID}
	}
	return nil
}
