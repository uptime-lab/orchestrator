package workflow_template_update

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateUpdateCommand represents a command to update a workflow template
type WorkflowTemplateUpdateCommand struct {
	ID          uuid.UUID                        `json:"id"`
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	IsShortcut  bool                             `json:"is_shortcut"`
	Steps       []workflow_template.WorkflowStep `json:"steps"`
	// Graph is the node-graph definition; when provided it is the source of
	// truth and Steps are derived from it server-side. An OMITTED graph means
	// UNCHANGED: the stored one survives and Steps are re-derived from it. It
	// deliberately does not clear the graph — a re-synthesized one is a bare
	// chain that has lost every condition, join, wait, policy and schedule
	// trigger, which silently and permanently unschedules the playbook.
	Graph *workflow_template.WorkflowGraph `json:"graph"`
	// OUID is the LEGACY single-unit field, kept so an existing client keeps
	// working; OUIDs supersedes it. Read them through ScopeOUIDs, never
	// directly — a caller that sent only one and a caller that sent a set must
	// end up in the same place.
	OUID *uuid.UUID `json:"ou_id"`
	// OUIDs scopes the playbook to a SET of organizational units; empty = all
	// nodes of the account.
	OUIDs []uuid.UUID `json:"ou_ids"`
	// Status is active or draft; empty keeps the default/current value
	Status string `json:"status"`
	// ExpectedUpdatedAt, when set, makes this a compare-and-swap: the update is
	// refused with ErrWorkflowTemplateStale if the stored row has moved on since
	// the client read it.
	//
	// This is a full-document PUT — every field the client holds is written back
	// — so without it one operator's save silently reverts another's. It is
	// OPTIONAL rather than required because a required precondition would break
	// every existing client at once; the editor sends it, and the API contract
	// says a caller that omits it accepts last-write-wins.
	ExpectedUpdatedAt *time.Time           `json:"expected_updated_at"`
	AuthContext       commands.AuthContext `json:"-"` // Set internally, not from request
}

// ScopeProvided reports whether this caller expressed a scope at all.
//
// It exists for the same reason Graph is a pointer: this is a full-document PUT,
// and an OMITTED scope means UNCHANGED, never "unscope". Without the
// distinction, a client that holds no scope field — an old integration, a script
// assembling the document by hand — silently widened a playbook scoped to one
// unit into a fleet-wide one, and on a cron playbook the next minute's run
// targeted every node in the account.
//
// An EXPLICIT `"ou_ids": []` still unscopes: that is a caller saying so, and the
// editor sends the field on every save. The legacy `"ou_id": null` no longer
// does — it is indistinguishable from omitting the field — so unscoping through
// the legacy field means sending `ou_ids: []` or using
// PUT /workflow-templates/:id/organizational-unit, which exists for exactly this.
func (c WorkflowTemplateUpdateCommand) ScopeProvided() bool {
	return c.OUIDs != nil || c.OUID != nil
}

// ScopeOUIDs is the scope the caller asked for, from either field. Only
// meaningful when ScopeProvided() is true.
//
// A client sending the legacy single `ou_id` and one sending `ou_ids` must land
// in the same place, and `ou_ids` wins when both are present: it is the field
// that can express "no units" as well as "these units", which `ou_id` cannot.
func (c WorkflowTemplateUpdateCommand) ScopeOUIDs() []uuid.UUID {
	if c.OUIDs != nil {
		return c.OUIDs
	}
	if c.OUID != nil {
		return []uuid.UUID{*c.OUID}
	}
	return nil
}
