package ssh_key_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SSHKeyGetQuery represents a query to get an SSH key by ID
type SSHKeyGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
