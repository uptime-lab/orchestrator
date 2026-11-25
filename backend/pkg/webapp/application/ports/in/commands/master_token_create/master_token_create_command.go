package master_token_create

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// MasterTokenCreateCommand represents a command to create a new master token
type MasterTokenCreateCommand struct {
	ID              uuid.UUID  `json:"-"` // Set internally, not from request
	Name            string     `json:"name"`
	DefaultUsername string     `json:"default_username"`
	DefaultPassword string     `json:"default_password"`
	SSHKeyID        *uuid.UUID `json:"ssh_key_id,omitempty"`
	// OUID: devices adopted with this token are placed into this OU.
	OUID        *uuid.UUID           `json:"ou_id,omitempty"`
	Tags        []string             `json:"tags,omitempty"`
	ExpiresAt   *time.Time           `json:"expires_at,omitempty"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}
