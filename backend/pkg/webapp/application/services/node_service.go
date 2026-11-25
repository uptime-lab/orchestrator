package services

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	domain_node_command "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// NodeDefaults carries default credentials from a master token to be applied to new nodes
type NodeDefaults struct {
	DefaultUsername string
	DefaultPassword string
	// DefaultPasswordUnreadable: the token holds a password no configured key
	// decrypts, so DefaultPassword is empty for a reason worth logging rather
	// than copying an accidental blank onto every node the token adopts.
	DefaultPasswordUnreadable bool
	SSHKeyID                  *uuid.UUID
	// OUID: the organizational unit freshly adopted nodes are placed into
	// (from the master token's adoption target).
	OUID *uuid.UUID
}

// NodeService defines the interface for node operations
type NodeService interface {
	Create(ctx context.Context, cmd node_create.NodeCreateCommand) (*node.Node, error)
	Update(ctx context.Context, cmd node_update.NodeUpdateCommand) (*node.Node, error)
	GetByID(ctx context.Context, query node_get.NodeGetQuery) (*node.Node, error)
	GetAllByAccountID(ctx context.Context, query node_list.NodeListQuery) ([]node.Node, error)
	// Delete removes a node. force is required when the node's device is ONLINE:
	// deleting it there is what strands the device, so the platform refuses
	// unless the caller says so explicitly — and a forced delete also asks the
	// agent to release its own adoption identity on the way out.
	Delete(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID, force bool) error
	// Deleted-node records. A delete removes the row without telling the device,
	// which then retries forever with an identity the platform no longer knows;
	// these three are how that device is taken back, or written off on purpose.
	ListDeleted(ctx context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error)
	// ListAdoptionRefusals returns the devices this account could not admit
	// because their hardware serial is bound to a node in another account —
	// derived from the platform's own refusal record, never stored.
	ListAdoptionRefusals(ctx context.Context, accountID uuid.UUID) ([]node.AdoptionRefusal, error)
	RestoreDeleted(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error)
	ForgetDeleted(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) error
	// FindDeletedBySerial recognises a stranded device at boot time. Global
	// rather than account-scoped, like GetByHardwareSerialGlobal beside it: a
	// booting device presents a serial and nothing else. Returns nil when the
	// serial is not a deleted node's.
	FindDeletedBySerial(ctx context.Context, serial string) *node_tombstone.Tombstone
	// RebootToRecovery performs a one-shot netboot: force the boot target to
	// the network bootstrap, reboot, and restore the previous target once the
	// node reconnects netbooted.
	RebootToRecovery(ctx context.Context, accountID, userID, nodeID uuid.UUID) error
	Connect(ctx context.Context, cmd node_connect.NodeConnectCommand) (*node.Node, error)
	GetAdoptionInfo(ctx context.Context, accountID, nodeID uuid.UUID, platformURL, grpcEndpoint string) (string, error)
	// CreateAdoptionCode mints the one-time exchange code behind the node's
	// one-command install link (revokes the node's previous code).
	CreateAdoptionCode(ctx context.Context, accountID, nodeID uuid.UUID) (string, time.Time, error)
	// ExchangeAdoptionCode consumes a one-time code and returns the adoption
	// blob; every failure mode maps to ErrAdoptionCodeInvalid (no oracle).
	ExchangeAdoptionCode(ctx context.Context, code, platformURL, grpcEndpoint string) (string, error)
	SendCommand(ctx context.Context, cmd node_command.NodeCommandCommand) error
	SendBatchCommand(ctx context.Context, cmd node_command.BatchNodeCommandCommand) (*node_command.BatchCommandResult, error)
	// ScanOSUsers dispatches the fixed OS-user inventory scan and returns the
	// command id to poll; GetOSUserScanResult returns the parsed inventory.
	ScanOSUsers(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error)
	GetOSUserScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.OSUserScanResult, error)

	// Tracked files: read a path off the device (track/refresh), poll that read,
	// stop following a file, and remove it from the device.
	ListTrackedFiles(ctx context.Context, nodeID uuid.UUID) ([]node_file.TrackedFile, error)
	GetTrackedFile(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.TrackedFile, error)
	ReadNodeFile(ctx context.Context, accountID, userID, nodeID uuid.UUID, path string, maxBytes int64) (uuid.UUID, error)
	GetNodeFileReadResult(ctx context.Context, accountID, userID, nodeID, commandID uuid.UUID, path string, maxBytes int64, trackedLimit int) (*node_file.ReadResult, error)
	SaveNodeFileVersion(ctx context.Context, accountID, nodeID uuid.UUID, path, content string) (*node_file.NodeFile, error)
	UntrackNodeFile(ctx context.Context, accountID, nodeID uuid.UUID, path string) error

	// Watchdog supervision: desired state, dispatch, and the facts the device
	// reports back.
	GetWatchdog(ctx context.Context, accountID, nodeID uuid.UUID) (*node.WatchdogState, error)
	SetWatchdog(ctx context.Context, accountID, userID, nodeID uuid.UUID, cfg node.WatchdogConfig) (*node.WatchdogState, error)
	RecordWatchdogApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error
	// LED behaviour (stealth / ACT+PWR off / ambient indication).
	GetLED(ctx context.Context, accountID, nodeID uuid.UUID) (*node.LEDState, error)
	SetLED(ctx context.Context, accountID, userID, nodeID uuid.UUID, cfg node.LEDConfig) (*node.LEDState, error)
	// ResetLED removes the node's own LED configuration so it inherits again.
	ResetLED(ctx context.Context, accountID, userID, nodeID uuid.UUID) (*node.LEDState, error)
	RecordLEDApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error
	// RecordIdentifyFailed clears the transient identify-registry blink for a
	// dispatched identify whose terminal report came back failed.
	RecordIdentifyFailed(accountID, nodeID uuid.UUID)
	RecordWatchdogDisarmed(ctx context.Context, accountID, nodeID uuid.UUID, reason string) error
	// RecordWatchdogRefused stores the agent's pre-arm verification refusal
	// ("<code>: <details>") without touching desired state.
	RecordWatchdogRefused(ctx context.Context, accountID, nodeID uuid.UUID, refusal, details string) error
	// Device profiles: declared hardware equipage, its ladder
	// (declared -> applied -> confirmed), and the locate blink.
	// DeviceProfileCatalog returns the loaded registry (nil when not wired).
	DeviceProfileCatalog() *device_profile.Catalog
	GetDeviceProfileState(ctx context.Context, accountID, nodeID uuid.UUID) (*node.DeviceProfileState, error)
	SetDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID, eq *device_profile.Equipage) (*node.DeviceProfileState, error)
	PreviewDeviceProfile(ctx context.Context, accountID, nodeID uuid.UUID) (*node.DeviceProfilePreview, error)
	// ApplyDeviceProfile returns (commandID, queued, error): a nil command id
	// with queued=true means the apply waits for the node's next managed-OS
	// connect — legitimate for a declarative apply.
	ApplyDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, bool, error)
	ScanDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error)
	GetDeviceProfileScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.DeviceProfileScanStatus, error)
	Identify(ctx context.Context, accountID, userID, nodeID uuid.UUID, durationSec int, stop bool) (uuid.UUID, int, error)
	// IdentifyStates returns the account's transient identify/button registry
	// (which nodes blink, whose button was pressed recently) — what a browser
	// opened after the fact hydrates from.
	IdentifyStates(accountID uuid.UUID) []node.IdentifyState
	// RecordDeviceProfileApplied stores the render hash an
	// apply_device_profile's terminal success confirmed (the hash from the
	// command's own payload — see the gRPC settle hook).
	RecordDeviceProfileApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error
	// RecordBootloaderFlashed stamps the settle time of a successful EEPROM
	// flash (update_boot_order) — the durable "reboot still owed" state the
	// UI reads back (see the gRPC settle hook).
	RecordBootloaderFlashed(ctx context.Context, accountID, nodeID uuid.UUID) error
	// RecordIdentityReleased stamps a deleted node's tombstone when the device
	// reports that it erased its adoption file (the release command's terminal
	// success, which arrives after the node row is gone).
	RecordIdentityReleased(ctx context.Context, accountID, nodeID uuid.UUID) error
	// NoteDeletedNodeRefused stamps a deleted node's tombstone when a device
	// presented that node's id AND matching token and was refused. Changes
	// nothing about the refusal; best-effort.
	NoteDeletedNodeRefused(ctx context.Context, accountID, nodeID uuid.UUID, presentedToken, presentedSerial string)
	// InvalidateAppliedStateAfterReimage forgets the device-profile, watchdog
	// and LED applied markers once a successful install_os has replaced the
	// disk they describe. Access is excluded on purpose — it has its own
	// self-heal, and two mechanisms for one fact drift apart.
	InvalidateAppliedStateAfterReimage(ctx context.Context, accountID, nodeID uuid.UUID) error
	// NotifyButtonPressed fans a physical button press out to the account's
	// browsers (transient locate signal; nothing is persisted).
	NotifyButtonPressed(ctx context.Context, accountID, nodeID uuid.UUID, pressKind string) error
	// RecordAccessApplied stores the access-config generation the DEVICE
	// confirmed it fully applied; ClearAccessApplied forgets it when an apply
	// only partly converged, so the platform makes no claim rather than a stale
	// one.
	//
	// `reported` is the agent's claim and is NOT stored as given: the
	// implementation accepts it only as a match against a hash the platform
	// computes itself, and stores its own value. Do not "simplify" that away —
	// the column decides whether the self-heal ever pushes this node again, so
	// an agent free to choose it can suppress a future revocation.
	RecordAccessApplied(ctx context.Context, accountID, nodeID uuid.UUID, reported string) error
	ClearAccessApplied(ctx context.Context, accountID, nodeID uuid.UUID) error
	// ScanWatchdogStatus probes the DEVICE for whether the watchdog daemon is
	// actually running, rather than reporting the last command's memory of it.
	ScanWatchdogStatus(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error)
	GetWatchdogStatusScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.WatchdogStatusScanResult, error)
	// RecordPlatformRenderedFile stores content the platform put on the node
	// as a tracked-file version (source=platform, status applied).
	RecordPlatformRenderedFile(ctx context.Context, accountID, nodeID uuid.UUID, path, content string) error
	// MarkTrackedFileApplied stamps last_applied_at once the DEVICE confirms a
	// write of a tracked file (a dispatch is not an apply).
	MarkTrackedFileApplied(ctx context.Context, accountID, nodeID uuid.UUID, path string) error
	// CompleteTrackedFileRead records a tracked-file read (track or refresh)
	// when the device's result arrives, so tracking does not depend on a
	// client polling for it. Any other read_file is left alone.
	CompleteTrackedFileRead(ctx context.Context, cmd *domain_node_command.NodeCommand, result map[string]string) error
	DeleteNodeFileOnDevice(ctx context.Context, accountID, userID, nodeID uuid.UUID, path, confirmPath string, backup bool) (uuid.UUID, error)
	// ScanEEPROMConfig dispatches a read of the live rpi-eeprom-config and
	// returns the command id; GetEEPROMConfigScanResult returns the parsed KEY=VALUE entries.
	ScanEEPROMConfig(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error)
	GetEEPROMConfigScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.EEPROMConfigScanResult, error)
	UpdateSystemInfo(ctx context.Context, nodeID, accountID uuid.UUID, systemInfo *node.SystemInfo) error
	SendUpdateFileCommand(ctx context.Context, fileID uuid.UUID, accountID uuid.UUID) error
	// Internal methods for gRPC server
	GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error)
	UpdateBootOrderFromAgent(ctx context.Context, nodeID, accountID uuid.UUID, bootOrder []string) error
	ClearAutoProvisionEEPROMPending(ctx context.Context, nodeID, accountID uuid.UUID) error
	// ForceNetbootAfterFailedInstall sends a node back to netboot when an OS
	// install fails, since the agent leaves the target deliberately unbootable
	// in that case. Also clears the reboot-to-recovery one-shot, which would
	// otherwise restore the old boot target on the next system-info report.
	ForceNetbootAfterFailedInstall(ctx context.Context, accountID, nodeID uuid.UUID) error
	// ForceNetbootAfterDiskErase does the same after a successful erase_drive
	// on a node running the recovery system, whose boot disk may now be
	// empty; on a node running its own OS it changes nothing.
	ForceNetbootAfterDiskErase(ctx context.Context, accountID, nodeID uuid.UUID) error
	// OSInstallInFlight reports an install_os to this node that has not
	// settled; the HTTP-boot decision serves recovery while it has not.
	OSInstallInFlight(ctx context.Context, nodeID uuid.UUID) (bool, error)
	// DefaultBootToOSOn sets boot_to_os=on when unset (post-provision default).
	DefaultBootToOSOn(ctx context.Context, nodeID, accountID uuid.UUID) error
	// ResolveSerialForBake answers, in one call, whether this account may bind a
	// serial (cross-account guard) and which of its own nodes carries it. Used
	// before signing a per-serial EEPROM, whose package path is keyed on the
	// serial alone. A full serial matches exactly — never through
	// GetByHardwareSerial's symmetric suffix arm. Returns
	// ErrHardwareSerialConflict for another account's serial, ErrNodeNotFound
	// when this account has no node with it.
	ResolveSerialForBake(ctx context.Context, accountID uuid.UUID, serial string) (*node.Node, error)
	StampWebUSBPlaceholder(ctx context.Context, nodeID, accountID uuid.UUID, shortSerial string) error
	// CommitWebUSBFlash reveals a flashing node as ready_for_adoption once the browser confirms the EEPROM write completed.
	CommitWebUSBFlash(ctx context.Context, nodeID, accountID uuid.UUID) error
	// MarkWebUSBReflashing puts a placeholder that is still awaiting its first
	// connect back into flashing for a re-flash. The node service's typed WebUSBDeviceConnectedError
	// (409) when the device connected after the caller read the row.
	MarkWebUSBReflashing(ctx context.Context, nodeID, accountID uuid.UUID) error
	UpdateAgentInfo(ctx context.Context, nodeID uuid.UUID, agentVersion, agentKind string, ledSources []string, lastSeen time.Time) error
	RotateAgentInstance(ctx context.Context, nodeID uuid.UUID, instanceID string) (node.AgentInstanceContinuity, error)
	MarkAwaitingReturn(ctx context.Context, nodeID uuid.UUID, slug string) error
	ApplyDeviceProfileToUnit(ctx context.Context, accountID, userID, ouID uuid.UUID) (*UnitDeviceProfileApplyReport, error)
	ClearAwaitingReturn(ctx context.Context, nodeID uuid.UUID) error
	UpdateHeartbeat(ctx context.Context, nodeID uuid.UUID, timestamp time.Time) error
	UpdateStatus(ctx context.Context, nodeID uuid.UUID, status string) error
	UpdateStatusWithAccount(ctx context.Context, nodeID, accountID uuid.UUID, status string) error
	// UpdateStatusWithAccountPinnedBy records which command imposed the status,
	// so a pin no reboot was asked for can be told apart from a real one.
	UpdateStatusWithAccountPinnedBy(ctx context.Context, nodeID, accountID uuid.UUID, status, slug string) error
	// GetStaleOnlineNodes finds nodes marked as online but haven't been seen recently (system-level, no account context)
	GetStaleOnlineNodes(ctx context.Context, threshold time.Time) ([]node.Node, error)

	// GetStaleTransientNodes finds nodes stuck in a command-induced status
	// ('restarting'/'maintenance') that the device is no longer expected to
	// clear (system-level, no account context)
	GetStaleTransientNodes(ctx context.Context, threshold time.Time) ([]node.Node, error)
	// ApproveNode approves a pending node (for master token authentication)
	// Returns ErrNodeNotFound if node doesn't exist or doesn't belong to account
	// Returns ErrNodeAlreadyApproved if node is already approved
	// Returns *entitlement.DeviceCapError (nothing written) when approving
	// would admit a device past the install's device cap, and
	// *entitlement.DeviceCapUnverifiedError (nothing written) when that cap
	// could not be checked
	ApproveNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error)
	// RejectNode rejects a pending node (for master token authentication)
	// Returns ErrNodeNotFound if node doesn't exist or doesn't belong to account
	// Returns ErrNodeNotPendingApproval if node is not pending approval
	RejectNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error)
	// ConnectWithMasterToken handles connection from an agent using master token authentication
	// Returns the existing or newly created node
	ConnectWithMasterToken(ctx context.Context, masterTokenID, accountID uuid.UUID, fingerprint node.HardwareFingerprint, defaults NodeDefaults) (*node.Node, error)
	// GetByHardwareSerial retrieves a node by its hardware serial (from fingerprint)
	GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, hardwareSerial string) (*node.Node, error)
	// GetByHardwareSerialGlobal retrieves a node by its hardware serial across all accounts.
	// Used by the unauthenticated HTTP-boot endpoint, which only knows the device serial.
	GetByHardwareSerialGlobal(ctx context.Context, hardwareSerial string) (*node.Node, error)
	// RegisterNetbootWithhold counts a withheld netboot attempt and reports whether
	// the boot-loop failsafe should serve recovery instead of withholding.
	RegisterNetbootWithhold(ctx context.Context, n *node.Node, globalThreshold int) (bool, error)
	// ClearBootFailsafe clears the boot-loop failsafe counter and flag (operator ack).
	ClearBootFailsafe(ctx context.Context, accountID, nodeID uuid.UUID) error
}

// UnitDeviceProfileApplyReport reports a fleet-wide device-profile apply over
// one organizational unit's directly-assigned nodes. Per-node outcomes are
// kept, because "dispatched to 7 of 9" with no names sends the operator to
// grep — a skip carries the node and the platform's own refusal wording.
type UnitDeviceProfileApplyReport struct {
	Dispatched int                     `json:"dispatched"`
	Queued     int                     `json:"queued"`
	Skipped    []UnitDeviceProfileSkip `json:"skipped"`
}

type UnitDeviceProfileSkip struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
