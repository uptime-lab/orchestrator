package node_create

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// NodeCreateCommand represents a command to create a new node
type NodeCreateCommand struct {
	ID              uuid.UUID            `json:"-"` // Set by handler, not from body
	Name            string               `json:"name"`
	SSHKeyID        uuid.UUID            `json:"ssh_key_id,omitempty"` // Optional: if not provided, default SSH key will be used
	DefaultUsername string               `json:"default_username"`     // Optional: console account provisioning creates; required when default_password is set
	DefaultPassword string               `json:"default_password"`     // Optional: console login password; needs default_username
	// ProvisionNetworkFirst opts the node into one-shot network-first EEPROM
	// provisioning: a per-node NetworkFirst override is set and the platform
	// flashes the EEPROM the first time the agent reports system info.
	ProvisionNetworkFirst bool                 `json:"provision_network_first,omitempty"`
	AuthContext           commands.AuthContext `json:"-"` // Set from context, not from body
}
