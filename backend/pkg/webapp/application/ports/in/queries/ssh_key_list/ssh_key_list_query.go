package ssh_key_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// SSHKeyListQuery represents a query to list all SSH keys for an account
type SSHKeyListQuery struct {
	AuthContext commands.AuthContext
	// Optional pagination parameters can be added here in the future
	// Offset int
	// Limit  int
}
