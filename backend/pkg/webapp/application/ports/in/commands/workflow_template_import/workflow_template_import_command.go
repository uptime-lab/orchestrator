package workflow_template_import

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateImportCommand imports a portable playbook document (an
// exported playbook or a built-in starter) into the account as a draft.
type WorkflowTemplateImportCommand struct {
	ID uuid.UUID `json:"-"` // Set internally, not from request
	// OUID is the LEGACY single-unit field, kept so an existing client keeps
	// working; OUIDs supersedes it. Read them through ScopeOUIDs, never
	// directly — a caller that sent only one and a caller that sent a set must
	// end up in the same place.
	OUID *uuid.UUID `json:"ou_id"`
	// OUIDs scopes the playbook to a SET of organizational units; empty = all
	// nodes of the account.
	OUIDs []uuid.UUID `json:"ou_ids"`
	// Document is the portable playbook document to import
	Document    workflow_template.PortableDocument `json:"document"`
	AuthContext commands.AuthContext               `json:"-"` // Set internally, not from request
}

// Validate checks the portable document envelope; graph content is validated
// by the service through the normal template Normalize/Validate path.
func (c *WorkflowTemplateImportCommand) Validate() error {
	if err := c.Document.ValidateDocument(); err != nil {
		return fmt.Errorf("invalid playbook document: %w", err)
	}
	return nil
}

// ScopeOUIDs is the scope the caller asked for, from either field.
//
// A client sending the legacy single `ou_id` and one sending `ou_ids` must land
// in the same place, and `ou_ids` wins when both are present: it is the field
// that can express "no units" as well as "these units", which `ou_id` cannot.
func (c WorkflowTemplateImportCommand) ScopeOUIDs() []uuid.UUID {
	if c.OUIDs != nil {
		return c.OUIDs
	}
	if c.OUID != nil {
		return []uuid.UUID{*c.OUID}
	}
	return nil
}
