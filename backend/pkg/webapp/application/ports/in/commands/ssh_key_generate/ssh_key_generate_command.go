package ssh_key_generate

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SSHKeyGenerateCommand asks the platform to generate a new connection keypair
// (ed25519): the public part is stored like any SSH key (and deployed to nodes),
// the private part is encrypted at rest and used for outbound SSH auth.
type SSHKeyGenerateCommand struct {
	ID          uuid.UUID            `json:"-"` // Set by handler, not from body
	Alias       string               `json:"alias"`
	Tags        []string             `json:"tags,omitempty"`
	AuthContext commands.AuthContext `json:"-"` // Set from context, not from body
}
