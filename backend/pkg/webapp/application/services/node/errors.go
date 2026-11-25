package node

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// Every refusal in this file is raised at ONE place — command dispatch — and
// surfaces through a dozen handlers, so each states its HTTP meaning here
// instead of relying on every handler to remember an arm for it
// (results.OperatorFacingError explains what that cost twice).
//
// The status follows the caller's next step: 400 when the REQUEST has to
// change, 409 when the request is fine and the target's state refuses it.
var (
	_ results.OperatorFacingError = (*AgentTooOldError)(nil)
	_ results.OperatorFacingError = (*DeviceBusyError)(nil)
	_ results.OperatorFacingError = (*WatcherNodeError)(nil)
	_ results.OperatorFacingError = (*WatcherInstallUnsafeError)(nil)
	_ results.OperatorFacingError = (*CommandPayloadTooLargeError)(nil)
	_ results.OperatorFacingError = (*RecoveryRebootRefusedError)(nil)
)

// AgentTooOldError reports a command refused because the node's agent predates
// it. A typed error rather than a formatted string so batch dispatch can tell
// "skipped, needs an agent update" apart from a genuine failure: at fleet scale
// the difference decides whether the operator updates agents or goes hunting for
// a bug.
type AgentTooOldError struct {
	NodeID  uuid.UUID
	Slug    string
	Have    string // reported agent version; empty when the agent never reported one
	Require string
}

func (e *AgentTooOldError) Error() string {
	if e.Have == "" {
		return fmt.Sprintf("node %s has not reported its agent version; %s requires %s or newer",
			e.NodeID, e.Slug, e.Require)
	}
	return fmt.Sprintf("node %s runs agent %s; %s requires %s or newer",
		e.NodeID, e.Have, e.Slug, e.Require)
}

// DeviceBusyError is returned when a command that owns a block device is
// dispatched to a node where another one is still running.
//
// It carries the in-flight command's id because that is the operator's next
// step: the answer to "why was my install refused" is "this one is still
// going", and without the id they cannot look at it.
type DeviceBusyError struct {
	NodeID    uuid.UUID
	Slug      string    // what was refused
	ActiveID  uuid.UUID // what is already running
	ActiveCmd string    // its slug — often, but not always, the same one
}

func (e *DeviceBusyError) Error() string {
	return fmt.Sprintf("node %s is already running %s (command %s), which is using its disk. "+
		"Wait for it to finish before running %s, or check that command's status if it looks stuck",
		e.NodeID, e.ActiveCmd, e.ActiveID, e.Slug)
}

// newAgentTooOldError fills in the version the operator needs to hear about.
// Constructing this bare produced "requires  or newer", which tells nobody
// which release to install.
func newAgentTooOldError(nodeID uuid.UUID, slug, have string) *AgentTooOldError {
	require, _ := node_command.MinAgentVersion(slug)
	return &AgentTooOldError{NodeID: nodeID, Slug: slug, Have: have, Require: require}
}

// WatcherNodeError reports a command refused because the node runs the
// telemetry-only watcher agent. The binary carries no
// command-execution code, so dispatching could only produce a timeout; the
// refusal names the remedy instead.
type WatcherNodeError struct {
	NodeID uuid.UUID
	Slug   string
}

func (e *WatcherNodeError) Error() string {
	return fmt.Sprintf("node %s runs the watcher agent and cannot execute commands (%s refused); "+
		"set its boot target to network boot and power-cycle it into recovery, or install the full agent",
		e.NodeID, e.Slug)
}

// OverlayRootError reports a write-shaped command refused because the node's
// root filesystem is an overlay (overlayroot): the write would land in the
// tmpfs upper layer, read back as applied, and evaporate at the next boot —
// with the recorded applied hash then suppressing the re-apply: write-shaped
// commands are otherwise blind to an overlay root filesystem.
type OverlayRootError struct {
	NodeID uuid.UUID
	Slug   string
}

func (e *OverlayRootError) Error() string {
	return fmt.Sprintf("node %s runs an overlay root filesystem, so %s would evaporate at the next reboot; "+
		"disable overlayroot (overlayroot-chroot / overlayroot=disabled) and reboot, or manage the node from netboot recovery", e.NodeID, e.Slug)
}

func (e *OverlayRootError) HTTPStatus() int   { return http.StatusConflict }
func (e *OverlayRootError) ErrorCode() string { return "overlay_root" }

var _ results.OperatorFacingError = (*OverlayRootError)(nil)

// SerialBoundElsewhereError reports a RESTORE refused because the deleted node's
// hardware serial is now carried by a node in another account. Unlike its
// neighbours in this file it is not raised at command dispatch, so it is
// declared with its own interface assertion.
//
// It is typed rather than logged because the silent version WAS the defect:
// RestoreDeleted used to warn and then restore the row without its fingerprint,
// which answers the operator 200 with a node that looks back while the device it
// names can never reach it. HTTP-boot resolves a booting device by serial alone,
// so a serial-less row is unreachable from netboot, and Connect refuses the
// device on its first bind, and the only trace is a log line.
//
// It names the SERIAL and never the owning account: the caller is holding the
// hardware, so the serial tells them nothing they do not already have, while the
// other account is a different tenant's state. Same reason the gRPC refusal and
// notifyAdoptionRefused name none.
type SerialBoundElsewhereError struct {
	NodeID uuid.UUID
	Serial string
}

func (e *SerialBoundElsewhereError) Error() string {
	return fmt.Sprintf("this node's hardware serial %s is registered to another account, "+
		"so restoring it would create a record no device can reach. "+
		"Remove the device from the account that holds it, then adopt it here again",
		e.Serial)
}

// The request is well-formed; the serial's current owner is what refuses it. The
// operator's next step is to free the serial, not to retry.
func (e *SerialBoundElsewhereError) HTTPStatus() int   { return http.StatusConflict }
func (e *SerialBoundElsewhereError) ErrorCode() string { return "serial_bound_elsewhere" }

var _ results.OperatorFacingError = (*SerialBoundElsewhereError)(nil)

// SerialBoundInAccountError refuses a RESTORE because the deleted node's
// hardware serial is carried by a live node of the SAME account: the board was
// adopted again as a new node. Restoring would leave two live rows in one
// account claiming one serial, and the boot lookup by serial could pick the
// stale one and decide that device's network boot. Unlike the cross-account
// refusal it names the live node, which is the caller's own.
type SerialBoundInAccountError struct {
	NodeID     uuid.UUID
	Serial     string
	HolderID   uuid.UUID
	HolderName string
}

func (e *SerialBoundInAccountError) Error() string {
	name := e.HolderName
	if name == "" {
		name = e.HolderID.String()
	}
	return fmt.Sprintf("this board (hardware serial %s) is now node %q in this account; "+
		"delete that node first if you want this one back", e.Serial, name)
}

func (e *SerialBoundInAccountError) HTTPStatus() int   { return http.StatusConflict }
func (e *SerialBoundInAccountError) ErrorCode() string { return "serial_bound_in_account" }

// ErrorDetails gives the UI the live node to link to.
func (e *SerialBoundInAccountError) ErrorDetails() any {
	return map[string]any{"node_id": e.HolderID.String(), "node_name": e.HolderName}
}

var (
	_ results.OperatorFacingError   = (*SerialBoundInAccountError)(nil)
	_ results.OperatorFacingDetails = (*SerialBoundInAccountError)(nil)
)

// NodeCredentialUnreadableError refuses a RESTORE whose stored device token
// cannot be used: restoring it would write a node no device can authenticate
// as, and an empty token is not even unique — a second such restore collides
// with the first.
//
// KeyProblem distinguishes the two causes, because the remedy differs: a
// ciphertext no configured key can read is recoverable by restoring the key,
// while a token that was already unreadable when the node was deleted left the
// record with no credential at all, and only a new adoption brings the device
// back.
type NodeCredentialUnreadableError struct {
	NodeID     uuid.UUID
	KeyProblem bool
}

func (e *NodeCredentialUnreadableError) Error() string {
	if e.KeyProblem {
		return "this node's stored credential cannot be decrypted with any configured key " +
			"(a SECRETS_ENCRYPTION_KEY problem); restore the key, then retry"
	}
	return "this node's credential could not be read when it was deleted, so a restore would create a node " +
		"its device cannot sign in as. Forget this record and adopt the device again"
}

// The request is fine; the record's state refuses it.
func (e *NodeCredentialUnreadableError) HTTPStatus() int   { return http.StatusConflict }
func (e *NodeCredentialUnreadableError) ErrorCode() string { return "node_credential_unreadable" }

var _ results.OperatorFacingError = (*NodeCredentialUnreadableError)(nil)

// WebUSBDeviceConnectedError refuses re-marking a WebUSB placeholder as
// flashing because its device connected after the caller read the row: a
// running device has just presented the identity the browser is about to
// bake. Moving it to flashing would take an admitted, connected device out of
// the device cap's count, so nothing is written. The request was fine and
// the node's state refuses it — 409; re-opening the flash dialog reads the
// node as connected and flashes without the re-mark.
type WebUSBDeviceConnectedError struct {
	NodeID uuid.UUID
}

func (e *WebUSBDeviceConnectedError) Error() string {
	return "this device connected to the platform while the flash was being prepared, so it was not marked as flashing; " +
		"reopen the flash dialog to continue"
}

func (e *WebUSBDeviceConnectedError) HTTPStatus() int   { return http.StatusConflict }
func (e *WebUSBDeviceConnectedError) ErrorCode() string { return "device_connected_during_flash" }

var _ results.OperatorFacingError = (*WebUSBDeviceConnectedError)(nil)

// WatcherInstallUnsafeError reports a Watcher (watcher) install refused
// because the device could not be managed again afterwards.
//
// It is its own type, and not a plain error string, for the reason
// AgentTooOldError is: the caller has to be able to tell "you asked for
// something this device cannot come back from" apart from a failure. The HTTP
// layer answers it 409 — the operator's next step is to fix the device's boot
// order, not to retry.
type WatcherInstallUnsafeError struct {
	NodeID uuid.UUID
	Reason string // written for an operator; names the boot order and what to do
}

// The remedy is stated with its own precondition, because the obvious phrasing
// ("update the bootloader first") sends the operator in a circle on exactly the
// hardware most likely to be refused: re-baking the EEPROM dispatches
// update_boot_order, and the agent stages that package on SD or USB media only
// — deliberately never on NVMe, where staging looks successful and is never
// applied. On an NVMe-only board that has netbooted, there is nothing to stage
// to, and the recommended fix fails with "no SD/USB boot media found".
func (e *WatcherInstallUnsafeError) Error() string {
	return fmt.Sprintf("node %s cannot return to recovery, so installing the watcher agent would leave it unmanageable: %s. "+
		"First provision its EEPROM with a network-first boot order; that needs SD or USB boot media in the device, "+
		"so on an NVMe-only board insert the provisioning card first. Installing the full agent is not affected",
		e.NodeID, e.Reason)
}

// NodeUnavailableError reports a command refused because the node's state
// cannot take it now: not approved, or in a status (offline, ready for
// adoption, …) that only queueable commands may wait out.
//
// Typed for the AgentTooOldError reason: as a formatted string it flattened
// into "failed to send command" with a 500, which tells the operator the
// platform broke when the request was fine and the node was simply away.
type NodeUnavailableError struct {
	NodeID uuid.UUID
	Slug   string
	// State is the node's status, or its approval status when NotApproved.
	State       string
	NotApproved bool
}

func (e *NodeUnavailableError) Error() string {
	if e.NotApproved {
		return fmt.Sprintf("node %s is not approved (current approval status: %s); approve it before sending %s",
			e.NodeID, e.State, e.Slug)
	}
	return fmt.Sprintf("node is not available (current status: %s); %s cannot wait for a node that is not connected — bring the node online and send it again",
		e.State, e.Slug)
}

// The request is well-formed; the node's state refuses it.
func (e *NodeUnavailableError) HTTPStatus() int { return http.StatusConflict }
func (e *NodeUnavailableError) ErrorCode() string {
	if e.NotApproved {
		return "node_not_approved"
	}
	return "node_unavailable"
}

var _ results.OperatorFacingError = (*NodeUnavailableError)(nil)

// NodeNotConnectedError reports a command refused because the node's status
// says it is reachable but no live stream is registered for it — the moment
// between a dropped connection and the monitor noticing. 503: retrying shortly
// is the right next step.
//
// Error() keeps the exact sentence handlers have long matched on, so the
// handlers that still compare strings keep answering it the same way.
type NodeNotConnectedError struct {
	NodeID uuid.UUID
}

func (e *NodeNotConnectedError) Error() string {
	return fmt.Sprintf("node %s is not connected", e.NodeID)
}

func (e *NodeNotConnectedError) HTTPStatus() int   { return http.StatusServiceUnavailable }
func (e *NodeNotConnectedError) ErrorCode() string { return "node_not_connected" }

var _ results.OperatorFacingError = (*NodeNotConnectedError)(nil)

// AgentAlreadyCurrentError refuses an update_agent that names the version the
// node already reports. Carrying it out reinstalls the same package and
// restarts the agent, which abandons every other command the node is running —
// a cost with nothing to show for it. A deliberate reinstall says so with
// "reinstall": true.
type AgentAlreadyCurrentError struct {
	NodeID  uuid.UUID
	Version string
}

func (e *AgentAlreadyCurrentError) Error() string {
	return fmt.Sprintf("node %s already runs agent %s; updating to the same version would only restart the agent. "+
		"Set \"reinstall\": true to reinstall it anyway", e.NodeID, e.Version)
}

// The request is well-formed; the node's state makes it a no-op.
func (e *AgentAlreadyCurrentError) HTTPStatus() int   { return http.StatusConflict }
func (e *AgentAlreadyCurrentError) ErrorCode() string { return "agent_already_current" }

var _ results.OperatorFacingError = (*AgentAlreadyCurrentError)(nil)

// CommandPayloadTooLargeError reports a command whose bulk field (write/patch
// content, a run_script body, a service unit) exceeds the per-frame ceiling. It
// is a typed error, and it must be raised BEFORE the command is registered and
// dispatched: an oversized gRPC frame does not fail the command, it tears down
// the node's stream, and because the receive loop survives, the node then shows
// ONLINE forever while every subsequent command (reboot, terminal, access
// re-apply, all workflow steps) silently fails. The tracked-file Apply path
// already enforces the same ceiling; this covers the generic POST /nodes/:id/
// command ingress that reaches the same writer.
type CommandPayloadTooLargeError struct {
	Field string
	Bytes int
	Limit int
}

func (e *CommandPayloadTooLargeError) Error() string {
	return fmt.Sprintf("command field %q is %d KB; the limit is %d KB. Reduce the content size.",
		e.Field, e.Bytes/1024, e.Limit/1024)
}

// HTTPStatus/ErrorCode: the node's agent cannot run this command, so the
// REQUEST has to change (a different command, or update the agent first).
func (e *AgentTooOldError) HTTPStatus() int   { return http.StatusBadRequest }
func (e *AgentTooOldError) ErrorCode() string { return "agent_too_old" }

// The request is well-formed; another command owns the disk. The caller waits
// for the one already running, whose id the message carries.
func (e *DeviceBusyError) HTTPStatus() int   { return http.StatusConflict }
func (e *DeviceBusyError) ErrorCode() string { return "device_busy" }

// The request is well-formed; this node's kind refuses every command. The
// remedy is in the message: netboot into recovery, or install the full agent.
func (e *WatcherNodeError) HTTPStatus() int   { return http.StatusConflict }
func (e *WatcherNodeError) ErrorCode() string { return "watcher_node" }

// The request is well-formed; the DEVICE could not be managed again after it.
// The remedy is to fix its boot order, not to retry.
func (e *WatcherInstallUnsafeError) HTTPStatus() int   { return http.StatusConflict }
func (e *WatcherInstallUnsafeError) ErrorCode() string { return "watcher_install_unsafe" }

// A client error, refused before dispatch: 400 rather than 413, which is what
// the generic command route answers — the ceiling is on a
// FIELD inside the body, not on the body, so a Content-Length client cannot act
// on 413 the way it can on this message.
func (e *CommandPayloadTooLargeError) HTTPStatus() int   { return http.StatusBadRequest }
func (e *CommandPayloadTooLargeError) ErrorCode() string { return "command_payload_too_large" }

// RecoveryRebootRefusedError reports a reboot-to-recovery refused BEFORE the
// node's boot target was touched. The one-shot pins boot_to_os to netboot and
// restores it only when the device reports a network boot; on a node that is
// not reachable, or whose boot order never reaches HTTP boot, that report never
// comes, and the pin would outlive the request as an override nobody asked for.
type RecoveryRebootRefusedError struct {
	NodeID uuid.UUID
	Code   string // "node_unavailable" or "recovery_unreachable"
	Reason string // written for an operator
}

func (e *RecoveryRebootRefusedError) Error() string {
	return fmt.Sprintf("node %s cannot be rebooted to recovery: %s", e.NodeID, e.Reason)
}

// The request is well-formed; the node's state refuses it. Nothing was
// changed, so the operator may retry once the state is different.
func (e *RecoveryRebootRefusedError) HTTPStatus() int   { return http.StatusConflict }
func (e *RecoveryRebootRefusedError) ErrorCode() string { return e.Code }

// RecoveryRequiredError refuses a command that only the netboot recovery
// bootstrap can run (capture_image, the offline patch and access apply) on a
// node that is running its installed OS. The agent refuses the same commands
// on arrival; refusing at dispatch is what can tell the operator the remedy,
// and for capture_image it is what keeps an image record from being created
// for a capture that cannot happen.
type RecoveryRequiredError struct {
	NodeID uuid.UUID
	Slug   string
	// Alternative names the live-OS counterpart, when there is one.
	Alternative string
}

func (e *RecoveryRequiredError) Error() string {
	msg := fmt.Sprintf("%s requires the node to be netbooted into recovery (use reboot-to-recovery first", e.Slug)
	if e.Alternative != "" {
		msg += ", " + e.Alternative
	}
	return msg + ")"
}

// The request is well-formed; the node's boot state refuses it.
func (e *RecoveryRequiredError) HTTPStatus() int   { return http.StatusConflict }
func (e *RecoveryRequiredError) ErrorCode() string { return "recovery_required" }

var _ results.OperatorFacingError = (*RecoveryRequiredError)(nil)

// BootDriveEraseError refuses an erase_drive aimed at the disk the node is
// running from, as the node itself reports it.
type BootDriveEraseError struct {
	NodeID uuid.UUID
	Device string
}

func (e *BootDriveEraseError) Error() string {
	return fmt.Sprintf("cannot erase boot drive: device %s contains the root filesystem", e.Device)
}

// The request is well-formed; what the node is running from refuses it.
func (e *BootDriveEraseError) HTTPStatus() int   { return http.StatusConflict }
func (e *BootDriveEraseError) ErrorCode() string { return "boot_drive" }

var _ results.OperatorFacingError = (*BootDriveEraseError)(nil)

// CaptureImageLimitError refuses a capture_image the account's image cap or
// stored-image quota has no room for. The wrapped error is the image service's
// own wording of which limit (ErrImageLimitReached), safe to show.
type CaptureImageLimitError struct {
	Err error
}

func (e *CaptureImageLimitError) Error() string { return e.Err.Error() }
func (e *CaptureImageLimitError) Unwrap() error { return e.Err }

// The request is well-formed; the account's stored images refuse it. The next
// step is to delete an image, not to retry.
func (e *CaptureImageLimitError) HTTPStatus() int   { return http.StatusConflict }
func (e *CaptureImageLimitError) ErrorCode() string { return "image_limit" }

var _ results.OperatorFacingError = (*CaptureImageLimitError)(nil)
