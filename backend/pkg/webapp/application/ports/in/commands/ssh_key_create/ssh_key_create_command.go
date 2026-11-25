package ssh_key_create

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SSHKeyCreateCommand represents a command to create a new SSH key
type SSHKeyCreateCommand struct {
	ID          uuid.UUID            `json:"-"` // Set internally, not from request
	Alias       string               `json:"alias"`
	KeyType     string               `json:"key_type"` // "ssh" or "ca", defaults to "ssh"
	KeyContent  string               `json:"key_content"`
	Tags        []string             `json:"tags,omitempty"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}
