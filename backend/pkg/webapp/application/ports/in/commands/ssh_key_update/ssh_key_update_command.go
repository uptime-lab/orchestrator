package ssh_key_update

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SSHKeyUpdateCommand represents a command to update an existing SSH key
type SSHKeyUpdateCommand struct {
	ID          uuid.UUID            `json:"-"`                     // Set internally, not from request
	Alias       *string              `json:"alias,omitempty"`       // Optional
	KeyContent  *string              `json:"key_content,omitempty"` // Optional
	IsDefault   *bool                `json:"is_default,omitempty"`  // Optional
	Tags        *[]string            `json:"tags,omitempty"`        // nil = unchanged, [] = clear
	AuthContext commands.AuthContext `json:"-"`                     // Set internally, not from request
}
