package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// UtilityAssignmentRepository persists utility assignments (one utility × one
// target × one config — docs/UTILITIES-PLAN.md §3.2). The per-(utility,
// target) uniqueness is a set of DB partial indexes, not a read-then-write, so
// two concurrent creates make one row and one clean 409.
type UtilityAssignmentRepository interface {
	Setup() error
	Create(ctx context.Context, a *utility.Assignment) error
	// Update rewrites config/enabled and the scope columns explicitly (a
	// re-target persists; a plain edit carries the loaded values).
	Update(ctx context.Context, a *utility.Assignment) error
	GetByID(ctx context.Context, accountID, id uuid.UUID) (*utility.Assignment, error)
	Delete(ctx context.Context, accountID, id uuid.UUID) error
	// ListByAccount returns every assignment of the account. Resolution filters
	// in the domain — the per-account row cap bounds what this can return.
	ListByAccount(ctx context.Context, accountID uuid.UUID) ([]utility.Assignment, error)
	// CountByAccount backs the per-account cap (fails open at the caller).
	CountByAccount(ctx context.Context, accountID uuid.UUID) (int64, error)
	// OUCounts returns per-OU assignment counts for the rail facet.
	OUCounts(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int64, error)
}

// NodeUtilityStateRepository persists the per-(node, utility) ladder rung.
// AppliedHash is written only from the settle path, from the platform's own
// dispatch payload (the access-generation rule).
type NodeUtilityStateRepository interface {
	Setup() error
	// SetDesired upserts the resolved desired hash for one utility on one node,
	// together with the pre-resolution source hash the OU rollup compares
	// against (see NodeState.DesiredSourceHash).
	SetDesired(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, desiredHash, desiredSourceHash string) error
	// RecordApplied stores the applied hash and clears the last error.
	RecordApplied(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, appliedHash string) error
	// RecordFailed stores the per-utility failure marker and CLEARS the applied
	// hash — a partial/failed apply must read as not-converged, never as the
	// hash it failed to reach (the access partial-apply rule).
	RecordFailed(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, message string) error
	ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]utility.NodeState, error)
	// ListByNodes is ListByNode for a set, so a per-unit rollup is one query
	// rather than one per node. An empty set returns nothing without querying —
	// a unit with no nodes must not turn into `node_id IN ()`.
	ListByNodes(ctx context.Context, accountID uuid.UUID, nodeIDs []uuid.UUID) ([]utility.NodeState, error)
	// DeleteMissing removes rungs for utilities no longer in the node's
	// resolved set (keep lists the ids that stay).
	DeleteMissing(ctx context.Context, accountID, nodeID uuid.UUID, keep []string) error
	// DeleteByNode removes every rung of a node (node delete).
	DeleteByNode(ctx context.Context, accountID, nodeID uuid.UUID) error
}
