package secret_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// SecretGetQuery fetches one secret by ID within the account.
type SecretGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
