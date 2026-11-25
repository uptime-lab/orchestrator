package secret_delete

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SecretDeleteCommand deletes a secret by ID within the account.
type SecretDeleteCommand struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
