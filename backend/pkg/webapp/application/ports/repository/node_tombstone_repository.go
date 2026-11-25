package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// NodeTombstoneRepository persists what a deleted node was, so a device still in
// the field can be taken back. See the domain package for why deleting a node
// otherwise strands its device permanently.
type NodeTombstoneRepository interface {
	// Create records a tombstone. Idempotent on the node id: deleting a node
	// twice is not possible, but a restore-then-delete cycle must overwrite the
	// old record rather than fail.
	Create(ctx context.Context, tombstone *node_tombstone.Tombstone) error

	// ListByAccountID returns an account's tombstones, newest deletion first.
	ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error)

	// GetByID returns one tombstone within an account, or ErrNotFound.
	GetByID(ctx context.Context, accountID, nodeID uuid.UUID) (*node_tombstone.Tombstone, error)

	// FindByHardwareSerial looks a tombstone up by the serial a booting device
	// presents. Deliberately NOT account-scoped: the HTTP-boot handler has no
	// account at that point — a device asks by serial alone, which is why the
	// node lookup beside it is global too. It is used only to recognise a
	// stranded device and never to serve it anything.
	FindByHardwareSerial(ctx context.Context, serial string) (*node_tombstone.Tombstone, error)

	// MarkRestored stamps restored_at, so a used tombstone stops being offered
	// while remaining readable as history.
	MarkRestored(ctx context.Context, accountID, nodeID uuid.UUID) error

	// MarkIdentityReleased stamps identity_released_at once, when the device
	// reports that it erased its adoption file. A replayed report must not move
	// the time, so the write is conditional on the column still being NULL.
	// ErrNotFound when no tombstone matches.
	MarkIdentityReleased(ctx context.Context, accountID, nodeID uuid.UUID, at time.Time) error

	// MarkRefused stamps last_refused_at, but only when the previous stamp is
	// older than minInterval: the write is the throttle's last line of defence
	// against a device redialling every second. A restored tombstone is never
	// stamped. Not stamping is not an error.
	MarkRefused(ctx context.Context, accountID, nodeID uuid.UUID, at time.Time, minInterval time.Duration) error

	// Delete forgets a tombstone: the operator's explicit "this device is never
	// coming back". The only other thing that removes one is an account purge.
	Delete(ctx context.Context, accountID, nodeID uuid.UUID) error
}
