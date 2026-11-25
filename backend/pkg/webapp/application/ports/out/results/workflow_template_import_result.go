package results

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateImportResult represents the outcome of importing a portable
// playbook document: the created draft plus human-readable warnings about
// references that could not be resolved in the importing account.
type WorkflowTemplateImportResult struct {
	Template *WorkflowTemplateResult `json:"template"`
	Warnings []string                `json:"warnings"`
}

// NewWorkflowTemplateImportResult creates a WorkflowTemplateImportResult
func NewWorkflowTemplateImportResult(template *workflow_template.WorkflowTemplate, warnings []string) *WorkflowTemplateImportResult {
	if warnings == nil {
		warnings = []string{}
	}
	return &WorkflowTemplateImportResult{
		Template: NewWorkflowTemplateResult(template),
		Warnings: warnings,
	}
}
