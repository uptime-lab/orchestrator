package repository

import (
	"context"

	"github.com/google/uuid"
)

// OUMutationRepository owns the organizational-unit mutations whose correctness
// depends on SERIALIZATION rather than on the value written — the ones where
// reading a guard and then acting on it in a second statement is the bug.
//
// It is separate from OrganizationalUnitRepository for the same reason
// MemberMutationRepository is separate from the member repository: the guarded
// operations need a transaction and row locks, and mixing them in with ordinary
// CRUD is how a later caller ends up composing the unguarded pieces again.
type OUMutationRepository interface {
	// DeleteIfUnreferenced deletes a unit only if it still has no children, no
	// nodes and no playbooks AT DELETE TIME, holding a row lock on the unit for
	// the whole check.
	//
	// The check-then-delete this replaces raced node assignment: two requests
	// could each see an empty unit and one could assign a node into it between
	// the other's check and its delete, leaving the node with a dangling
	// `ou_id`. With network-first boot that is not cosmetic — the HTTP-boot handler then 404s
	// `boot.sig` for that node forever, so a network-first device can never boot
	// again and reads as dead hardware.
	//
	// Returns ErrOrganizationalUnitNotFound / ErrOrganizationalUnitHasChild /
	// ErrOrganizationalUnitHasNodes / ErrOrganizationalUnitHasPlaybooks.
	DeleteIfUnreferenced(ctx context.Context, accountID, ouID uuid.UUID) error

	// AssignNodeOU points a node at a unit (nil clears it) while holding a SHARE
	// lock on that unit's row, which is the other half of the pair above: the
	// share lock blocks the delete's exclusive lock, so the unit cannot vanish
	// between "it exists" and the write. It updates the `ou_id` column ONLY —
	// never a full-row save, which would revert machine-owned columns another
	// path wrote seconds earlier.
	AssignNodeOU(ctx context.Context, accountID, nodeID uuid.UUID, ouID *uuid.UUID) error
}
