package master_token_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// MasterTokenGetQuery represents a query to get a master token by ID
type MasterTokenGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
