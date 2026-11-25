package utility

import (
	"time"

	"github.com/google/uuid"
)

// NodeState is the per-(node, utility) rung of the ladder: what was resolved
// for the node (desired) against what the device confirmed (applied). One row
// per utility per node — per-utility on purpose, so one refused utility never
// clears converged siblings (docs/UTILITIES-PLAN.md).
type NodeState struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	NodeID    uuid.UUID
	UtilityID string
	// DesiredHash is the last hash the platform resolved for this node.
	DesiredHash string
	// DesiredSourceHash is the hash of the same config BEFORE {{secrets.*}} /
	// {{node.attributes.*}} resolution, written by the same apply. It exists
	// for the OU rollup, which must answer "did the assignment change since
	// the last apply" without resolving secrets per node — resolution stamps
	// last_used_at, and a page load faking secret usage is worse than a stale
	// rung. A config edit or a re-scope moves this hash; a secret VALUE edit
	// does not, and that residual staleness is deliberate: the per-node view
	// resolves fresh and stays the authority. Empty on rows last applied
	// before the column existed.
	DesiredSourceHash string
	// AppliedHash is written ONLY at the command settle, from the platform's
	// own dispatch payload — never the agent's claim as given. Empty means
	// never applied (or cleared after a partial/failed apply, which is the
	// truth the ladder must show).
	AppliedHash string
	// LastError carries the per-utility failure marker from the last apply
	// ("failed: ..."), cleared on success.
	LastError string
	// DriftFailures: consecutive failed applies of the same desired source
	// hash (the reconcile loop's stand-down counter).
	DriftFailures int
	UpdatedAt     time.Time
	CreatedAt     time.Time
}

// LadderStatus is the operator-facing rung, derived — never stored.
type LadderStatus string

const (
	// StatusDeclared: resolved for the node, never applied.
	StatusDeclared LadderStatus = "declared"
	// StatusUpdatePending: applied once, but the desired hash moved since.
	StatusUpdatePending LadderStatus = "update_pending"
	// StatusApplied: the device confirmed the current desired hash.
	StatusApplied LadderStatus = "applied"
	// StatusFailed: the last apply failed for this utility.
	StatusFailed LadderStatus = "failed"
)

// Status derives the rung.
func (s *NodeState) Status() LadderStatus {
	switch {
	case s.LastError != "":
		return StatusFailed
	case s.AppliedHash == "":
		return StatusDeclared
	case s.AppliedHash != s.DesiredHash:
		return StatusUpdatePending
	default:
		return StatusApplied
	}
}
