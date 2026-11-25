package node_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// NodeGetQuery represents a query to get a node by ID
type NodeGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
