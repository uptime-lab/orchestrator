package results

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowStarterResult represents one built-in starter playbook in API
// responses. Document is the full portable playbook: the client instantiates
// a starter by posting it back to the import endpoint.
type WorkflowStarterResult struct {
	ID          string                             `json:"id"`
	Category    string                             `json:"category"`
	Name        string                             `json:"name"`
	Description string                             `json:"description"`
	BlockTypes  []string                           `json:"block_types"`
	Document    workflow_template.PortableDocument `json:"document"`
}

// NewWorkflowStarterResult creates a WorkflowStarterResult from a catalog entry
func NewWorkflowStarterResult(starter workflow_template.StarterTemplate) *WorkflowStarterResult {
	blockTypes := (&workflow_template.WorkflowTemplate{Graph: starter.Document.Graph}).BlockTypes()
	return &WorkflowStarterResult{
		ID:          starter.ID,
		Category:    starter.Category,
		Name:        starter.Document.Name,
		Description: starter.Document.Description,
		BlockTypes:  blockTypes,
		Document:    starter.Document,
	}
}
