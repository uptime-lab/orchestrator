package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/google/uuid"
)

// AccessEntryCounts is one rail bucket's counts, split by entry type so the UI
// can show the number relevant to the active tab (Users vs CA Certificates) —
// a mixed total matches no tab's visible rows and reads as a broken counter.
type AccessEntryCounts struct {
	Users int64
	CAs   int64
}

// AccessEntryRepository persists declarative access entries and their key
// references (the access_entry_keys join table). Reads hydrate each entry's
// KeyIDs; the Password field is decrypted on read and must be redacted by
// callers before it reaches the API.
type AccessEntryRepository interface {
	// Create persists a new entry (encrypting its password) plus any initial
	// KeyIDs as join rows.
	Create(ctx context.Context, e *access_entry.AccessEntry) error
	// Update replaces the mutable fields (username, sudo, password, principals,
	// adopt flag). Key references are managed via AttachKey/DetachKey, not here.
	Update(ctx context.Context, e *access_entry.AccessEntry) error
	// GetByID returns a single entry with KeyIDs hydrated and password decrypted.
	GetByID(ctx context.Context, accountID, id uuid.UUID) (*access_entry.AccessEntry, error)
	// Delete removes an entry and its join rows.
	Delete(ctx context.Context, accountID, id uuid.UUID) error

	// ListByOU / ListByNode / ListAccountLevel return entries for a single
	// target, KeyIDs hydrated. ListAccountLevel returns account-wide default
	// entries (no OU/node, account_wide=true); ListUnassigned returns the
	// no-target entries that are NOT account-wide (they apply to no node).
	ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]access_entry.AccessEntry, error)
	ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]access_entry.AccessEntry, error)
	ListAccountLevel(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error)
	ListUnassigned(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error)
	// ListAll returns every entry regardless of scope — for account-wide
	// overviews (e.g. which units trust which CA).
	ListAll(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error)
	// ListByOUs is the batch helper the effective-access resolver uses to load a
	// node's whole OU chain in one query (avoids N+1).
	ListByOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) ([]access_entry.AccessEntry, error)

	// AttachKey / DetachKey add or remove a single key reference. AttachKey is
	// idempotent. Per-key operations (not whole-list replacement) so a stale
	// client can never silently clobber a set it never saw.
	AttachKey(ctx context.Context, accountID, entryID, keyID uuid.UUID) error
	DetachKey(ctx context.Context, accountID, entryID, keyID uuid.UUID) error

	// DeleteByOUID removes every entry targeting an OU (OU deletion cleanup).
	DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error
	// DetachNode clears the node target of every entry scoped to a deleted node
	// (node deletion cleanup). The entry survives as unassigned — same reasoning
	// as DeleteByOUID: it carries an operator-authored password and key set.
	DetachNode(ctx context.Context, accountID, nodeID uuid.UUID) error
	// DeleteKeyReferences removes every join row for an SSH key across all
	// entries (SSH-key deletion cascade). Emptying a user entry's set locks that
	// user everywhere the entry applies.
	DeleteKeyReferences(ctx context.Context, accountID, keyID uuid.UUID) error

	// CountByOU returns per-OU entry counts plus the unassigned bucket (applies
	// nowhere), the account-default bucket (fleet-wide) and the account total,
	// for the credentials filter rail. Counts are split by entry type so the
	// rail can show the number matching the active tab.
	CountByOU(ctx context.Context, accountID uuid.UUID) (counts map[uuid.UUID]AccessEntryCounts, unassigned, accountWide AccessEntryCounts, total int64, err error)
}
