package organizational_unit_delete

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// OrganizationalUnitDeleteCommand deletes a unit by ID within the account.
type OrganizationalUnitDeleteCommand struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
