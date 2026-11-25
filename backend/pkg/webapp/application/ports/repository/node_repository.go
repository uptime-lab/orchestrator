package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// ReleasedSerialLease is one never-admitted binding the lease sweep released:
// enough to write the audit row and tell the account, and nothing that needs
// decrypting.
type ReleasedSerialLease struct {
	NodeID         uuid.UUID
	AccountID      uuid.UUID
	HardwareSerial string
}

// SerialHolder names the row behind a cross-account serial refusal: enough to
// record the refusal, nothing a caller could hand to the refused account.
type SerialHolder struct {
	AccountID uuid.UUID
	NodeID    uuid.UUID
}

// SerialBindingScope is what one hardware serial's binding looks like from a
// given account's side: the two facts, and nothing else. Deliberately not a node
// or a count — see SerialBindingScopes for why anything wider is a hazard on a
// read path.
type SerialBindingScope struct {
	// InAccount: a node in the ASKING account carries this serial.
	InAccount bool
	// InAnotherAccount: a node in some OTHER account carries it.
	InAnotherAccount bool
}

// AccountSerialHolder is a node of the ASKING account that carries a given
// serial: its id and name, which the asking account already sees in its own
// node list.
type AccountSerialHolder struct {
	NodeID uuid.UUID
	Name   string
}

// DeviceCapAdmission is what CreateUnderDeviceCap observed while admitting.
// Count is the admitted count read under the lock (before this insert);
// AccountCount is the admitting account's, read only while an account limit
// applies.
type DeviceCapAdmission struct {
	Count        int64
	AccountCount int64
}

// AdmissionLimits is every bound one admission must pass, checked together
// under the one admission lock: the instance-wide device cap and the
// admitting account's own limit. Either at 0 is no bound; both at 0 is a
// plain write that takes no lock.
type AdmissionLimits struct {
	// InstanceCap is the install's effective device cap; 0 = unlimited.
	InstanceCap int
	// AccountID is the account the admitted row belongs to.
	AccountID uuid.UUID
	// Account is that account's limit in force; Account.Limit 0 = none.
	Account entitlement.AccountLimit
}

// InstanceCapOnly is the limits of an install with no per-account limit.
func InstanceCapOnly(cap int) AdmissionLimits { return AdmissionLimits{InstanceCap: cap} }

// Bounded reports whether any limit applies, i.e. whether the admission
// must count under the lock.
func (l AdmissionLimits) Bounded() bool {
	return l.InstanceCap > 0 || (l.AccountID != uuid.Nil && l.Account.Applies())
}

// NodeRepository defines the interface for node-related database operations
type NodeRepository interface {
	// Create creates a new node
	Create(ctx context.Context, node *node.Node) error

	// CountByAccountID counts the account's nodes, for the per-account fleet cap.
	// A tombstoned (deleted) node is NOT counted: the row is gone, and a
	// restore deliberately does not re-check the cap — see the cap's call site.
	CountByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)

	// CountAdmittedNodes counts the devices this INSTALL manages, across every
	// account, for the device entitlement: rows that are approved and not in
	// a pre-connect status (node.IsAdmitted). No account predicate on purpose
	// — the free cap is per install.
	CountAdmittedNodes(ctx context.Context) (int64, error)

	// CountAdmittedNodesForAccount is the same predicate as
	// CountAdmittedNodes, restricted to one account: what a tenant is told
	// about its own fleet. The instance-wide count is the install
	// operator's number and never leaves the operator's card.
	CountAdmittedNodesForAccount(ctx context.Context, accountID uuid.UUID) (int64, error)

	// CreateUnderDeviceCap is the courtesy check on the row-creating paths,
	// and the admission itself for the one insert that writes an admitted
	// row (the restore of a node that was admitted when it was deleted):
	// it inserts a node only while the instance-wide
	// admitted count is below cap, with the count and the insert in ONE
	// transaction under a constant advisory lock, so concurrent admissions
	// serialize instead of all reading the same count. cap <= 0 means
	// unlimited and is a plain insert. At the cap it returns
	// *entitlement.DeviceCapError (nothing inserted). With an account limit
	// in limits the account's own admitted count is checked in the same
	// transaction, after the instance count: at it,
	// *entitlement.AccountDeviceLimitError (nothing inserted).
	//
	// A lock or a count that FAILS refuses too, with
	// *entitlement.DeviceCapUnverifiedError: the transaction is rolled back
	// and nothing is written, because a write made without the check would
	// let a database that cannot answer — a statement or lock timeout on the
	// connection is enough — lift the cap. Limits that bound nothing never
	// count, so they never fail this way.
	CreateUnderDeviceCap(ctx context.Context, node *node.Node, limits AdmissionLimits) (DeviceCapAdmission, error)

	// UpdateUnderDeviceCap and ApproveUnderDeviceCap are the two ADMISSION
	// writes: a row whose stored state is not admitted becoming admitted —
	// the first connect out of a pre-connect status (a full-row Update), and
	// the approval of a pending row. Same lock, same transaction, same
	// refusal on a failed lock or count as CreateUnderDeviceCap; the count excludes the row
	// being admitted. Refused, nothing is written, so the row stays
	// un-admitted and can be admitted later.
	UpdateUnderDeviceCap(ctx context.Context, node *node.Node, limits AdmissionLimits) (DeviceCapAdmission, error)
	ApproveUnderDeviceCap(ctx context.Context, nodeID uuid.UUID, limits AdmissionLimits) (DeviceCapAdmission, error)
	// UpdateStatusUnderDeviceCap is the status-only admission: the write
	// UpdateStatus/UpdateStatusPinnedBy refuse with
	// *node.AdmissionRequiredError, made under the same lock and count.
	UpdateStatusUnderDeviceCap(ctx context.Context, nodeID uuid.UUID, status, slug string, limits AdmissionLimits) (DeviceCapAdmission, error)

	// Update updates an existing node. It is never an admission: a new state
	// that is admitted is written only over a stored row that already is
	// (node.IsAdmitted), and anything else returns
	// *node.AdmissionRequiredError with nothing written. The full-row save is
	// a read-modify-write, and a copy read before the row stepped out of the
	// admitted set would otherwise put it back past the device cap.
	Update(ctx context.Context, node *node.Node) error

	// UpdateOperatorEditable writes only the columns a human edits (name, notes,
	// provisioning defaults). Use it for anything driven by an operator request:
	// a full-row Update from a row read moments earlier reverts the columns the
	// gRPC layer writes continuously, including the `restarting` pin.
	UpdateOperatorEditable(ctx context.Context, node *node.Node) error

	// UpdateNodeColumns writes exactly the named columns (plus updated_at). The
	// general form of the rule above, for operator actions that change a small
	// known set — policy flags, the watchdog's desired config — and must not
	// carry a whole stale row back to the database with them. It refuses the
	// status and approval_status columns: those decide admission, and each
	// has a writer that knows the device cap (UpdateStatus,
	// ApproveUnderDeviceCap, CommitWebUSBFlash).
	UpdateNodeColumns(ctx context.Context, accountID, nodeID uuid.UUID, columns map[string]interface{}) error

	// UpdateSystemReport is the device-side twin of UpdateNodeColumns: it
	// writes only the named columns of a system-info report (from a fixed
	// allowlist of report-owned columns), taking their values from node, plus
	// updated_at. With completeRecoveryRevert it also restores boot_to_os from
	// node and clears the reboot-to-recovery bookkeeping, but only on a row
	// that still has that revert pending; the return reports whether it did.
	// A full-row save from a report would revert any operator column write
	// that landed between the report's read and its save.
	UpdateSystemReport(ctx context.Context, node *node.Node, columns []string, completeRecoveryRevert bool) (bool, error)

	// GetByID retrieves a node by ID within an account
	GetByID(ctx context.Context, accountID, nodeID uuid.UUID) (*node.Node, error)

	// GetAllByAccountID retrieves all nodes for a specific account
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]node.Node, error)

	// Delete deletes a node by ID within an account
	Delete(ctx context.Context, accountID, nodeID uuid.UUID) error

	// GetByIDAndAccount retrieves a node by ID and account ID (for registration validation)
	GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error)

	// (There is deliberately no GetByUniqueToken: it had no callers — the
	// connect path fetches by node id and compares tokens in constant time —
	// and its plaintext equality match was the one thing that stood between
	// unique_token and encryption at rest.)

	// UpdateStatus updates only the status field, and clears the pin cause.
	// It never admits: moving an approved row from a pre-connect status to
	// any other is refused with *node.AdmissionRequiredError and nothing is
	// written (UpdateStatusUnderDeviceCap is that write).
	UpdateStatus(ctx context.Context, nodeID uuid.UUID, status string) error
	// UpdateStatusPinnedBy updates the status and records the command slug that
	// imposed it (node.Node.PinnedBySlug). Same admission rule as UpdateStatus.
	UpdateStatusPinnedBy(ctx context.Context, nodeID uuid.UUID, status, slug string) error

	// CommitWebUSBFlash moves a node from flashing to ready_for_adoption and
	// stamps bootloader_flashed_at, in ONE statement conditional on the
	// stored status still being flashing. A device that connected (and was
	// admitted) after the browser read the row is left untouched: an
	// unconditional write would take it out of the device cap's count while
	// it stays connected. Reports whether the row was committed.
	CommitWebUSBFlash(ctx context.Context, accountID, nodeID uuid.UUID, flashedAt time.Time) (bool, error)
	// MarkWebUSBReflashing moves a node awaiting its first connect back to
	// flashing for a re-flash, in ONE statement conditional on the row still
	// awaiting that connect and not being in a connected status. A device
	// whose first connect landed after the browser read the row is left
	// untouched: the unconditional write it replaces moved a connected,
	// admitted device to flashing, out of the device cap's count while its
	// stream stayed up. Reports whether the row was marked.
	MarkWebUSBReflashing(ctx context.Context, accountID, nodeID uuid.UUID) (bool, error)

	// UpdateHeartbeat updates the last heartbeat timestamp
	UpdateHeartbeat(ctx context.Context, nodeID uuid.UUID, timestamp time.Time) error

	// UpdateAgentInfo updates agent version, agent kind and last seen. Empty
	// version or kind means "leave unchanged" — most callers stamp only the
	// timestamp.
	UpdateAgentInfo(ctx context.Context, nodeID uuid.UUID, agentVersion, agentKind string, ledSources []string, lastSeen time.Time) error
	RotateAgentInstance(ctx context.Context, nodeID uuid.UUID, instanceID string) (previous string, err error) // store the connecting agent process's instance id, return the previous session's
	MarkAwaitingReturn(ctx context.Context, nodeID uuid.UUID, slug string, since time.Time) error              // the platform expects this node back after <slug>
	ClearAwaitingReturn(ctx context.Context, nodeID uuid.UUID) error                                           // reconnect-side counterpart

	// GetStaleOnlineNodes finds nodes marked as online but haven't been seen recently
	// Returns nodes with status='online' and last_seen_at older than the threshold
	GetStaleOnlineNodes(ctx context.Context, threshold time.Time) ([]node.Node, error)

	// GetStaleTransientNodes finds nodes stuck in a command-induced status
	// ('restarting'/'maintenance') with last_seen_at older than the threshold.
	// Deliberately excludes 'shutdown', which is a deliberate end state.
	GetStaleTransientNodes(ctx context.Context, threshold time.Time) ([]node.Node, error)

	// MarkAllOnlineNodesOffline marks all nodes with status 'online' as 'offline'
	// This should be called on server startup to reset stale connection states
	MarkAllOnlineNodesOffline(ctx context.Context) (int64, error)

	// GetByHardwareSerial retrieves a node by hardware serial (from fingerprint JSONB) within an account
	// Used for master token authentication
	GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, hardwareSerial string) (*node.Node, error)

	// GetByHardwareSerialGlobal retrieves a node by hardware serial across all accounts.
	// Hardware serials are globally unique (one physical device belongs to one account),
	// so this is used by the unauthenticated HTTP-boot endpoint, which only knows the
	// serial and has no account/token context. Returns ErrNodeNotFound if no node matches.
	GetByHardwareSerialGlobal(ctx context.Context, hardwareSerial string) (*node.Node, error)

	// GetAwaitingFirstConnectBySerialSuffix finds a WebUSB placeholder (awaiting
	// its first connect, stamped with the short serial) whose stored serial is
	// the low word of the connecting device's full serial. Returns
	// ErrNodeNotFound if none matches.
	GetAwaitingFirstConnectBySerialSuffix(ctx context.Context, accountID uuid.UUID, fullSerial string) (*node.Node, error)

	// ListByExactSerial returns every node (across all accounts) whose stored
	// hardware serial equals the given NORMALIZED serial (case-insensitive
	// exact match, no suffix fuzziness). Used by the cross-account serial
	// uniqueness guard; an empty slice means the serial is unclaimed.
	ListByExactSerial(ctx context.Context, normalizedSerial string) ([]*node.Node, error)

	// LowWordHolderInAnotherAccount returns a node of an account other than
	// accountID whose stored serial collides with the given NORMALIZED serial
	// by its low word without being equal to it, or nil when none does: for a
	// full 16-hex serial, another full serial with the same low word or a short
	// hex stamp the serial ends in; for a short stamp, a full serial ending in
	// it. Short against short is not a collision — unrelated devices
	// legitimately share a low word — and equality is the exact guard's
	// (ListByExactSerial). Only the two ids are read, so nothing is decrypted.
	LowWordHolderInAnotherAccount(ctx context.Context, accountID uuid.UUID, normalizedSerial string) (*SerialHolder, error)

	// SerialBindingScopes answers, for each NORMALIZED serial, only the two
	// questions the cross-account guard asks. Serials no node carries at all are
	// absent from the map.
	//
	// It exists so a READ path never reaches for ListByExactSerial. That one
	// hydrates whole node rows, and toDomain AES-GCM-decrypts unique_token and
	// default_password on every one of them — so answering "is this serial bound
	// elsewhere" with it would pull ANOTHER tenant's device credentials into
	// memory, once per row, per page render. This returns two booleans collapsed
	// in Postgres: no foreign node id, account id, name or count crosses the
	// boundary, and nothing is decrypted.
	SerialBindingScopes(ctx context.Context, accountID uuid.UUID, normalizedSerials []string) (map[string]SerialBindingScope, error)

	// SerialHoldersInAccount answers, for each NORMALIZED serial, which node of
	// THIS account carries it (the most recently created one when several do).
	// Serials no node of the account carries are absent. Rows of other accounts
	// are never read, and only id and name are selected, so nothing is
	// decrypted.
	SerialHoldersInAccount(ctx context.Context, accountID uuid.UUID, normalizedSerials []string) (map[string]AccountSerialHolder, error)

	// ReleaseUnclaimedSerialLeases clears the fingerprint of every node that
	// has never been admitted — awaiting its first connect, or still pending
	// approval — carries a full 16-hex serial, and has shown no life since
	// unseenSince (last seen, last heartbeat, or created, whichever is
	// latest). The ROW stays: the operator's placeholder is still theirs, it
	// just no longer holds a serial nobody has presented. One statement, so a
	// concurrent connect either sees the fingerprint or does not; the guard
	// then re-binds it on the device's next connect through the ordinary
	// first-bind path.
	ReleaseUnclaimedSerialLeases(ctx context.Context, unseenSince time.Time) ([]ReleasedSerialLease, error)

	// UpdateApprovalStatus updates the approval status of a node
	UpdateApprovalStatus(ctx context.Context, nodeID uuid.UUID, status string) error

	// IncrementBootAttempt atomically counts one withheld netboot attempt (with a
	// debounce to collapse a single boot's duplicate requests and a window that
	// starts a fresh series after a quiet period). Returns the resulting count and
	// whether this call actually counted (false = debounced).
	IncrementBootAttempt(ctx context.Context, nodeID uuid.UUID, window, debounce time.Duration) (int, bool, error)
	// SetBootFailsafe records that the failsafe served recovery and one-shot-resets
	// the counter to resetCountTo (so the next boot withholds again).
	SetBootFailsafe(ctx context.Context, nodeID uuid.UUID, at time.Time, resetCountTo int) error
	// ResetBootFailsafe clears the failsafe counter and flag.
	ResetBootFailsafe(ctx context.Context, nodeID uuid.UUID) error

	// GetPendingApprovalNodes retrieves all nodes pending approval for an account
	GetPendingApprovalNodes(ctx context.Context, accountID uuid.UUID) ([]node.Node, error)
}
