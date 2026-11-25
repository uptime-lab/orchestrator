package workflow_template_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// WorkflowTemplateListQuery represents a query to list workflow templates
type WorkflowTemplateListQuery struct {
	AuthContext commands.AuthContext
	Name        *string // Optional name filter (partial match)
	IsShortcut  *bool   // Optional shortcut filter
}
