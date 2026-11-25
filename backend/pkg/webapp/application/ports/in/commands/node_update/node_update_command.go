package node_update

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// NodeUpdateCommand represents a command to update an existing node
// Note: Hostname and IPv4 are intentionally excluded - they can only be set during node registration
type NodeUpdateCommand struct {
	ID              uuid.UUID            `json:"-"`                          // Set from URL parameter, not from body
	Name            *string              `json:"name,omitempty"`             // Optional
	SSHKeyID        *uuid.UUID           `json:"ssh_key_id,omitempty"`       // Optional: SSH key ID for provisioning
	DefaultUsername *string              `json:"default_username,omitempty"` // Optional: Default username for provisioning
	DefaultPassword *string              `json:"default_password,omitempty"` // Optional: Default password for provisioning
	Notes           *string              `json:"notes,omitempty"`            // Optional: free-form operator notes
	AuthContext     commands.AuthContext `json:"-"`                          // Set from context, not from body
}

// Validate checks the command's own constraints. It is called by the service
// rather than by an echo.Validator — none is installed — so every caller of
// Update gets the check, not only the HTTP handler.
func (c NodeUpdateCommand) Validate() error {
	// A supplied name must name the node: an empty one leaves a row every
	// list renders as a blank line and no search can find. Omitting the field
	// still means "unchanged".
	if c.Name != nil && strings.TrimSpace(*c.Name) == "" {
		return app_errors.InvalidRequest("invalid_node_name", errors.New("name must not be empty"))
	}
	if c.Notes != nil {
		if err := node.ValidateNotes(*c.Notes); err != nil {
			return err
		}
	}
	return nil
}
