package workflow_execution_start

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// WorkflowExecutionStartCommand represents a command to start a workflow execution
type WorkflowExecutionStartCommand struct {
	TemplateID  uuid.UUID            `json:"template_id"`
	NodeIDs     []uuid.UUID          `json:"node_ids"` // List of node IDs to execute workflow on
	AuthContext commands.AuthContext `json:"-"`        // Set internally, not from request
	// Scheduled marks executions fired by the cron scheduler (no human
	// actor). Set internally, never from the request.
	Scheduled bool `json:"-"`
}
