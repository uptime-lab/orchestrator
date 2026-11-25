package organizational_unit_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// OrganizationalUnitGetQuery fetches one unit by ID within the account.
type OrganizationalUnitGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
