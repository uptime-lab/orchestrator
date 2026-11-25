package node_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// NodeListQuery represents a query to list all nodes for an account
type NodeListQuery struct {
	AuthContext commands.AuthContext
	// Optional pagination parameters can be added here in the future
	// Offset int
	// Limit  int
}
