package workflow_execution_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// WorkflowExecutionGetQuery represents a query to get a workflow execution by ID
type WorkflowExecutionGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
