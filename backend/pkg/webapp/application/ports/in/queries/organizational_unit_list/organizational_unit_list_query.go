package organizational_unit_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// OrganizationalUnitListQuery lists all units for the account.
type OrganizationalUnitListQuery struct {
	AuthContext commands.AuthContext
}
