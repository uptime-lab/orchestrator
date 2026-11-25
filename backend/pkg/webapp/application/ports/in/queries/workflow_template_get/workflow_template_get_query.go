package workflow_template_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// WorkflowTemplateGetQuery represents a query to get a workflow template by ID
type WorkflowTemplateGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
