package node_command

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// NodeCommandCommand represents a command to send a command to a node
type NodeCommandCommand struct {
	NodeID      uuid.UUID              `json:"-"`                // Set from URL parameter, not from body
	Command     string                 `json:"command"`          // "reboot", "shutdown", "status_check"
	Params      map[string]interface{} `json:"params,omitempty"` // Optional key:value params (e.g., {"delay_seconds": 0})
	AuthContext commands.AuthContext   `json:"-"`                // Set from context, not from body
}
