package master_token_update

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// MasterTokenUpdateCommand represents a command to update a master token
type MasterTokenUpdateCommand struct {
	ID              uuid.UUID  `json:"-"` // From URL param
	Name            *string    `json:"name,omitempty"`
	IsActive        *bool      `json:"is_active,omitempty"`
	DefaultUsername *string    `json:"default_username,omitempty"`
	DefaultPassword *string    `json:"default_password,omitempty"`
	// SSHKeyID: nil = unchanged, "" = clear (adopted devices get no key from
	// this token), UUID string = set. A null in JSON cannot mean "clear": it
	// decodes to the same nil as an absent field.
	SSHKeyID *string `json:"ssh_key_id,omitempty"`
	// OUID: nil = unchanged, "" = clear, UUID string = set (adoption target OU).
	OUID        *string              `json:"ou_id,omitempty"`
	Tags        *[]string            `json:"tags,omitempty"` // nil = unchanged, [] = clear
	ExpiresAt   *time.Time           `json:"expires_at,omitempty"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}
