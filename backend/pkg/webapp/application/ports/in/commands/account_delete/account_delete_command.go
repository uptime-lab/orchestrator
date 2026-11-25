package account_delete

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// AccountDeleteCommand represents a command to delete an account
type AccountDeleteCommand struct {
	ID          uuid.UUID            `json:"id"`
	AuthContext commands.AuthContext `json:"-"`
}
