package utility

import (
	"time"

	"github.com/google/uuid"
)

// Scope names an assignment's target kind.
type Scope string

const (
	// ScopeAccount delivers to every node of the account ("all nodes").
	ScopeAccount Scope = "account"
	// ScopeOU delivers to the nodes of ONE organizational unit (and, through
	// inheritance, to nodes of its child units unless a nearer level overrides).
	ScopeOU Scope = "ou"
	// ScopeNode delivers to one node.
	ScopeNode Scope = "node"
	// ScopeUnassigned applies to no node — the safe default for an assignment
	// created but not yet targeted (the access-entry rule).
	ScopeUnassigned Scope = "unassigned"
)

// Assignment binds one utility to one target with one config. The OU target
// is a SET (utility_assignment_ous join table, the access-entry model, a
// product decision): one config legitimately spans several
// units — the same kiosk URL across three cafés — without duplicating rows.
// Phase 1 shipped a single-OU scope for tie-freedom; the tie-freedom now
// lives in the SERVICE instead (no two assignments of one utility may share
// an OU), exactly where access entries keep their username-overlap rule.
type Assignment struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	UtilityID string
	// Scope: exactly one of AccountWide / OUIDs / NodeID is set, or none for
	// unassigned. Validate enforces the XOR.
	AccountWide bool
	// OUIDs is the unit set (hydrated from the join table). The legacy single
	// ou_id column is backfilled into the join once in Setup() and never read
	// again — the access_entry_ous / workflow_template_ous precedent.
	OUIDs  []uuid.UUID
	NodeID *uuid.UUID
	// Config is the operator's param values for this target (defaults NOT
	// applied here — an omitted key stays omitted so the schema default can
	// evolve; ResolvedConfig applies them at resolution).
	Config map[string]interface{}
	// Enabled=false keeps the row and its config but excludes it from
	// resolution entirely — "present but off", distinct from unassigned.
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Scope derives the target kind from the fields. Derived, never stored —
// two fields that must agree is how state drifts.
func (a *Assignment) Scope() Scope {
	switch {
	case a.AccountWide:
		return ScopeAccount
	case a.NodeID != nil:
		return ScopeNode
	case len(a.OUIDs) > 0:
		return ScopeOU
	default:
		return ScopeUnassigned
	}
}

// ValidateScope refuses an assignment naming more than one target kind.
func (a *Assignment) ValidateScope() error {
	targets := 0
	if a.AccountWide {
		targets++
	}
	if len(a.OUIDs) > 0 {
		targets++
	}
	if a.NodeID != nil {
		targets++
	}
	if targets > 1 {
		return ErrScopeConflict
	}
	return nil
}
