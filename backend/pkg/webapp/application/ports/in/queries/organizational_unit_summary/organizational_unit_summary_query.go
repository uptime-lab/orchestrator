package organizational_unit_summary

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// OrganizationalUnitSummaryQuery asks for the one-call overview of a unit —
// its policies resolved with sources, and a first-N sample of everything the
// unit delivers (nodes, access entries, secrets, playbooks, adoption tokens).
// The Organization page renders a whole unit from this single response.
type OrganizationalUnitSummaryQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
