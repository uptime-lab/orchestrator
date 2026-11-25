// Package node_attention decides which of an account's nodes need an
// operator's attention, and says why in machine-readable terms.
//
// Every check here is a pure function of facts the CALLER has already loaded.
// That is the point of the package, not an accident of style: the Status
// section resolves a whole account in one pass, and a check that reached for a
// repository would turn one page load into an O(nodes) query storm — the trap
// services/utility/ou_ladder.go was written to avoid. A new check that cannot
// be answered from Facts belongs in the service, which loads its input once
// for the whole fleet and hands it in.
//
// Two rules the checks must keep.
//
// An unanswerable check is SKIPPED, never answered. When the agent release
// listing or the EEPROM signer cannot be read, the caller leaves the
// corresponding field unset and names the kind in its Omitted list. "No
// attention items" and "we could not look" are different facts, and reporting
// the second as the first is how a fleet reads as healthy while it is not.
//
// Wording lives in the frontend. An Item carries a kind and the raw values
// behind it (a version pair, a slug, a count) — never a sentence. One place
// spells the operator's words, and it is the one that can translate them.
package node_attention

import (
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Kind identifies a condition. The string is the API contract and the i18n
// key suffix, so it is renamed only with the frontend in the same change.
type Kind string

const (
	// KindOffline: the node is not connected. Emitted only when nothing more
	// specific applies — a node the platform is waiting to come back from a
	// reboot gets KindAwaitingReturn instead, which is the honest word.
	KindOffline Kind = "offline"
	// KindAwaitingReturn: offline, and the platform is still waiting for it to
	// return from a named command (install_os, reboot, …).
	KindAwaitingReturn Kind = "awaiting_return"
	// KindNeverConnected: approved and adopted, but has never reported in.
	KindNeverConnected Kind = "never_connected"
	// KindPendingApproval: registered through a master token and waiting for a
	// human to approve it.
	KindPendingApproval Kind = "pending_approval"
	// KindAgentOutdated: a newer agent release is published on this install.
	KindAgentOutdated Kind = "agent_outdated"
	// KindBootloaderOutdated: the device's EEPROM firmware is behind the base
	// the signer would bake from.
	KindBootloaderOutdated Kind = "bootloader_outdated"
	// KindBootloaderRebootPending: new firmware has been written to the device
	// and takes effect on its next boot.
	//
	// It exists because the flash does NOT reboot: the agent answers "Signed
	// EEPROM image flashed successfully. Reboot required for the new boot order
	// to take effect", and the platform never reboots by itself. Without this
	// state the node keeps reporting the OLD firmware version, so
	// bootloader_outdated stays true and the row goes on inviting a re-flash of
	// a device that is only waiting for its reboot.
	KindBootloaderRebootPending Kind = "bootloader_reboot_pending"
	// KindInRecovery: the node is running the recovery bootstrap, so most
	// managed-OS work is held until it boots its installed OS.
	KindInRecovery Kind = "in_recovery"
	// KindBootFailsafe: the boot-loop failsafe served recovery because the node
	// kept asking to netboot without ever booting its OS.
	KindBootFailsafe Kind = "boot_failsafe"
	// KindBootAttemptsFailing: a boot loop in progress — withheld netboot
	// attempts are accumulating, but the failsafe threshold has not been hit.
	KindBootAttemptsFailing Kind = "boot_attempts_failing"
	// KindRecoveryRevertPending: a one-shot reboot-to-recovery is still in
	// flight; the node's previous boot target is restored when it reports back
	// netbooted.
	KindRecoveryRevertPending Kind = "recovery_revert_pending"
	// KindWatchdogDisarmed: the on-device breaker took the watchdog down by
	// itself. A disarm the platform asked for is not this.
	KindWatchdogDisarmed Kind = "watchdog_disarmed"
	// KindUtilityDrift: a utility assigned to this node is not on the rung its
	// assignment asks for.
	KindUtilityDrift Kind = "utility_drift"
	// KindDeviceProfilePending: hardware is declared for this node and has
	// never been applied to the device.
	KindDeviceProfilePending Kind = "device_profile_pending"
	// KindDeviceProfileRebootPending: the hardware settings have been written
	// and the device has not rebooted since.
	//
	// The same shape as the bootloader's, and for the same reason: applying a
	// profile writes config.txt, the hardware comes up on the next boot, and
	// the platform never reboots by itself. DeviceProfileRebootPending is the
	// pure test, and it is deliberately tri-state — a node that has reported
	// neither an apply time nor a boot time is not judged.
	KindDeviceProfileRebootPending Kind = "device_profile_reboot_pending"
	// KindDanglingOU: the node points at a unit that no longer exists. On a
	// network-first device this is not cosmetic — HTTP boot answers 404 for its
	// boot signature, so it can never boot again and reads as dead hardware.
	KindDanglingOU Kind = "dangling_ou"
	// KindWatcherUnrecoverable: a telemetry-only watcher whose reported boot
	// order can no longer reach HTTP boot. A watcher executes no commands, so
	// recovery is the only way back and this device has lost it.
	KindWatcherUnrecoverable Kind = "watcher_unrecoverable"
	// KindTokenUnreadable: no configured key can decrypt this node's device
	// credential.
	KindTokenUnreadable Kind = "token_unreadable"
	// KindAccessApplyFailed: the last attempt to put this node's shell access
	// on the device failed.
	//
	// Access config self-heals — the platform re-applies on every managed-OS
	// report where the confirmed generation does not match the resolved one —
	// and it STANDS DOWN after three consecutive failures for the same hash.
	// Today that stand-down is an ERROR log line and nothing else, and the code
	// that writes it says why that matters: "a stood-down node is one an
	// operator has to go look at, and a silent stand-down would read as
	// converged". This reports the first failure rather than the third: it is
	// strictly earlier, it costs no extra query, and by the time the loop has
	// given up the operator has already had two chances to see it.
	//
	// What it does NOT claim is that access is currently wrong. A revocation
	// that failed to land is the case worth the row, and a failure followed by
	// a success is not reported at all — only the latest attempt counts.
	KindAccessApplyFailed Kind = "access_apply_failed"
	// KindCommandStuck: a command the node accepted has said nothing for far
	// longer than any of them take.
	//
	// The shape a LOST terminal report leaves behind. One sequence that
	// produces it: an agent update replaces the agent's own binary, systemd
	// restarts it, and a utility apply running in the same process fails while
	// it is shutting down — the failure reaches the device's journal and never
	// the platform. The abandonment sweep runs on RECONNECT and the
	// replacement agent has already reconnected, so nothing sweeps it: the
	// command stays in_progress on a node that is online the whole time, and
	// every action on that node stays withheld because the platform believes
	// it is busy.
	KindCommandStuck Kind = "command_stuck"
	// KindDeviceCapReached: the install admits as many devices as its
	// entitlement covers, and this node is waiting to be admitted — a node
	// whose device has not connected yet, or a pending device. Its first
	// connect, or its approval, will be refused until a slot is free. Current
	// and Expected carry the admitted count and the cap.
	KindDeviceCapReached Kind = "device_cap_reached"
	// KindAccountDeviceLimitReached: the account holds as many admitted
	// devices as its own per-account limit allows, and this node is waiting
	// to be admitted. Reported only where the instance cap is not reached
	// (that one is checked first at admission, and is the refusal the node
	// would meet). Expected carries the account's limit, AccountDevices its
	// admitted count — both the account's own numbers.
	KindAccountDeviceLimitReached Kind = "account_device_limit_reached"
)

// Severity separates "this device may be lost" from "an operator should get to
// this". Only the first four are critical, and each is critical because the
// device cannot be recovered from the platform alone.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
)

// severities is the one place a kind's weight is decided. A kind missing here
// is a warning, so a new check is never silently loud.
var severities = map[Kind]Severity{
	KindOffline:              SeverityCritical,
	KindDanglingOU:           SeverityCritical,
	KindWatcherUnrecoverable: SeverityCritical,
	KindTokenUnreadable:      SeverityCritical,
}

// SeverityFor reports how loud a kind is.
func SeverityFor(k Kind) Severity {
	if s, ok := severities[k]; ok {
		return s
	}
	return SeverityWarning
}

// AllKinds is the inventory the API publishes so the UI can build its filter
// without a second copy of this list. Order is the filter's order: the loudest
// and the two the operator asked for by name first.
func AllKinds() []Kind {
	return []Kind{
		KindOffline,
		KindAwaitingReturn,
		KindNeverConnected,
		KindPendingApproval,
		KindAgentOutdated,
		KindBootloaderOutdated,
		KindBootloaderRebootPending,
		KindInRecovery,
		KindBootFailsafe,
		KindBootAttemptsFailing,
		KindRecoveryRevertPending,
		KindWatchdogDisarmed,
		KindUtilityDrift,
		KindDeviceProfilePending,
		KindDeviceProfileRebootPending,
		KindAccessApplyFailed,
		KindDanglingOU,
		KindWatcherUnrecoverable,
		KindTokenUnreadable,
		KindCommandStuck,
		KindDeviceCapReached,
		KindAccountDeviceLimitReached,
	}
}

// Item is one condition found on one node.
type Item struct {
	Kind     Kind
	Severity Severity
	// Current and Expected carry a version pair where the check is a
	// comparison (agent, bootloader). Empty otherwise.
	Current  string
	Expected string
	// Subject names what the condition is about: a command slug, a utility id,
	// the breaker's reason, the boot order that cannot reach HTTP boot.
	Subject string
	// Count carries a number the condition is about (failed boot attempts).
	Count int
	// Since is when the condition started, where the platform knows it. Nil is
	// common and honest: most of these have no recorded start.
	Since *time.Time
	// AccountDevices and InstanceOperator are set on device_cap_reached
	// (AccountDevices also on account_device_limit_reached):
	// the caller's own admitted count, and whether the caller operates the
	// install. A caller who does not is never given the install-wide count
	// (Current stays empty for it).
	AccountDevices   *int64
	InstanceOperator *bool
}

// ActiveCommand is a command the platform has out to a node and has not seen
// settle.
type ActiveCommand struct {
	Slug string
	// UpdatedAt is the last thing the node said about it.
	UpdatedAt time.Time
}

// UtilityDrift is one non-converged utility on one node, resolved by the
// caller from node_utility_states. The rung is not recomputed here: the
// per-node view resolves secrets to answer it exactly, and a page load faking
// secret usage is worse than a coarse rollup (the reasoning is on
// utility.NodeState.DesiredSourceHash).
type UtilityDrift struct {
	UtilityID string
	// Failed marks a utility whose last apply failed, as opposed to one that
	// is merely behind.
	Failed bool
}

// Facts is everything a check may read. Fields the caller could not resolve
// stay unset, and the check that needs them does not run.
type Facts struct {
	// LatestAgentVersion is the newest agent release published on this install.
	// Empty means the listing could not be read — the agent check is skipped,
	// never answered "up to date".
	LatestAgentVersion string
	// BaseFirmware maps SoC ("cm4"/"cm5") to the bootloader version the signer
	// would bake from. Nil when no signer is configured, and the bootloader
	// check is then skipped.
	BaseFirmware map[string]string
	// KnownOUs is the account's live unit ids. Nil disables the dangling-unit
	// check rather than declaring every node dangling.
	KnownOUs map[uuid.UUID]bool
	// Drift is the non-converged utilities per node id.
	Drift map[uuid.UUID][]UtilityDrift
	// Active is the unsettled command each node is carrying, where it has one.
	Active map[uuid.UUID]ActiveCommand
	// AccessApplyFailed marks a node whose most recent access-config apply
	// ended in failure. The caller decides what "most recent" means; this
	// package only reports it.
	AccessApplyFailed map[uuid.UUID]bool
	// DeviceCap is the install's admitted count against its effective device
	// cap. Nil when either could not be read: the cap check is then skipped.
	DeviceCap *DeviceCap
	// AccountDeviceLimit is the account's admitted count against its own
	// per-account limit. Nil when no such limit binds the account, or when
	// it could not be read: the check is then skipped.
	AccountDeviceLimit *AccountDeviceLimit
	// Now anchors the age comparisons, so a test does not need a clock.
	Now time.Time
}

// DeviceCap is the instance-wide admission state the cap check reads.
type DeviceCap struct {
	// Cap is the effective cap; 0 is unlimited.
	Cap int
	// Admitted is the install's admitted-device count. It decides Reached,
	// and only the instance operator is shown it: on a shared install it is
	// the sum of other tenants' fleets.
	Admitted int64
	// AccountDevices is the caller's own account's admitted count.
	AccountDevices int64
	// InstanceOperator: the caller operates this install.
	InstanceOperator bool
}

// Reached reports whether the next admission would be refused.
func (c DeviceCap) Reached() bool {
	return c.Cap > 0 && c.Admitted >= int64(c.Cap)
}

// AccountDeviceLimit is one account's admission state under its own limit.
type AccountDeviceLimit struct {
	// Limit is the account's limit in force; 0 is none.
	Limit int
	// AccountDevices is the account's admitted count.
	AccountDevices int64
}

// Reached reports whether the account's next admission would be refused.
func (l AccountDeviceLimit) Reached() bool {
	return l.Limit > 0 && l.AccountDevices >= int64(l.Limit)
}

// neverConnectedGrace keeps a node that was adopted seconds ago out of the
// list. Adoption, first boot and the first gRPC connect take minutes on real
// hardware, and a row that appears and disappears on its own trains an
// operator to ignore the list.
const neverConnectedGrace = time.Hour

// commandStuckAfter bounds how long a command may say nothing before the list
// calls it stuck. Generous on purpose: an OS install and a capture legitimately
// run for a long time without reporting, and crying stuck over those would
// teach an operator to ignore the row.
const commandStuckAfter = 30 * time.Minute

// Evaluate returns every condition that holds for one node, loudest first.
// An empty result means the node is fine as far as these checks can see.
func Evaluate(n *node.Node, f Facts) []Item {
	if n == nil {
		return nil
	}
	var items []Item
	add := func(k Kind, it Item) {
		it.Kind = k
		it.Severity = SeverityFor(k)
		items = append(items, it)
	}

	// Reachability. These are mutually exclusive on purpose: a node the
	// platform is waiting on, one that has never arrived and one that simply
	// went away are three different next steps for an operator, and listing
	// two of them for the same device makes the list longer without making it
	// more useful.
	switch {
	case n.IsPendingApproval():
		add(KindPendingApproval, Item{Since: &n.CreatedAt})
	case n.AwaitingReturnAfter != "" && !n.IsInConnectedState():
		add(KindAwaitingReturn, Item{Subject: n.AwaitingReturnAfter, Since: n.AwaitingReturnSince})
	case n.LastSeenAt == nil:
		if f.Now.Sub(n.CreatedAt) > neverConnectedGrace {
			add(KindNeverConnected, Item{Since: &n.CreatedAt})
		}
	case !n.IsInConnectedState():
		add(KindOffline, Item{Since: n.LastSeenAt})
	}

	// Software that is behind. Both are comparisons against something outside
	// the node row, so both are skipped when the caller could not resolve it.
	if u := agentUpdate(n, f.LatestAgentVersion); u != nil {
		add(KindAgentOutdated, *u)
	}
	// A flash that has not taken effect preempts the freshness verdict. The
	// device still REPORTS the old version — it is running it — so asking
	// bootloaderUpdate here would answer "outdated" and put a re-flash button
	// in front of an operator whose next step is a reboot.
	if p := n.BootloaderRebootPending(); p != nil && *p {
		add(KindBootloaderRebootPending, Item{Since: n.BootloaderFlashedAt})
	} else if u := bootloaderUpdate(n, f.BaseFirmware); u != nil {
		add(KindBootloaderOutdated, *u)
	}

	// Boot state. The failsafe one-shot-resets the attempt counter when it
	// fires, so the two boot checks cannot both hold — but the guard is
	// written anyway, because that reset is a property of a different file.
	if n.BootFailsafeAt != nil {
		add(KindBootFailsafe, Item{Since: n.BootFailsafeAt})
	} else if n.BootAttemptCount > 0 {
		add(KindBootAttemptsFailing, Item{Count: n.BootAttemptCount})
	}
	// Only for a node that is actually reporting: IsRecoveryBootstrap reads the
	// last system-info report, and for an offline node that report describes a
	// boot that may be days old.
	if n.IsInConnectedState() && n.IsRecoveryBootstrap() {
		add(KindInRecovery, Item{})
	}
	if n.RecoveryRevertPending {
		add(KindRecoveryRevertPending, Item{})
	}

	// A disarm the platform asked for is not a fault, and showing it as one
	// resurfaces every deliberate disarm as an alert.
	if n.WatchdogDisarmReason != "" && !node.IsPlatformDisarmReason(n.WatchdogDisarmReason) {
		add(KindWatchdogDisarmed, Item{Subject: n.WatchdogDisarmReason})
	}

	for _, d := range f.Drift[n.ID] {
		add(KindUtilityDrift, Item{Subject: d.UtilityID})
	}

	switch {
	case n.DeviceProfile != nil && n.DeviceProfileAppliedHash == "":
		add(KindDeviceProfilePending, Item{})
	default:
		// Applied, but the hardware only comes up on the next boot. Nil is
		// "cannot tell" and is left alone: the platform fails open on this
		// question everywhere else too.
		if pending := n.DeviceProfileRebootPending(); pending != nil && *pending {
			add(KindDeviceProfileRebootPending, Item{Since: n.DeviceProfileAppliedAt})
		}
	}

	if f.KnownOUs != nil && n.OUID != nil && !f.KnownOUs[*n.OUID] {
		add(KindDanglingOU, Item{Subject: n.OUID.String()})
	}

	if n.IsWatcher() {
		if ok, reason := node.BootOrderCanReturnToNetboot(n.ReportedBootOrder()); !ok {
			add(KindWatcherUnrecoverable, Item{Subject: reason})
		}
	}

	if n.UniqueTokenUnreadable {
		add(KindTokenUnreadable, Item{})
	}

	if f.AccessApplyFailed[n.ID] {
		add(KindAccessApplyFailed, Item{})
	}

	// A node the cap will refuse when it asks to be admitted. A rejected
	// device is not waiting for anything, and an admitted one is never
	// refused. The instance cap is asked first, as admission asks it; the
	// account's own limit is reported only when the install has room.
	waiting := !n.IsAdmitted() && !n.IsRejected()
	instanceReached := f.DeviceCap != nil && f.DeviceCap.Reached()
	if waiting && !instanceReached && f.AccountDeviceLimit != nil && f.AccountDeviceLimit.Reached() {
		own := f.AccountDeviceLimit.AccountDevices
		add(KindAccountDeviceLimitReached, Item{
			Expected:       strconv.Itoa(f.AccountDeviceLimit.Limit),
			AccountDevices: &own,
		})
	}
	if instanceReached && waiting {
		accountDevices, operator := f.DeviceCap.AccountDevices, f.DeviceCap.InstanceOperator
		it := Item{
			Expected:         strconv.Itoa(f.DeviceCap.Cap),
			AccountDevices:   &accountDevices,
			InstanceOperator: &operator,
		}
		if operator {
			it.Current = strconv.FormatInt(f.DeviceCap.Admitted, 10)
		}
		add(KindDeviceCapReached, it)
	}

	if active, ok := f.Active[n.ID]; ok &&
		!active.UpdatedAt.IsZero() &&
		f.Now.Sub(active.UpdatedAt) > commandStuckAfter {
		since := active.UpdatedAt
		add(KindCommandStuck, Item{Subject: active.Slug, Since: &since})
	}

	return items
}

// agentUpdate answers whether a newer agent release exists for this node, or
// nil when the question does not apply or cannot be answered.
//
// A watcher is excluded deliberately. update_agent is a command, a watcher
// executes none, and offering an operator an update they cannot perform is
// worse than saying nothing. Getting a watcher current is a recovery boot, not
// a row in this list.
//
// A node on the recovery bootstrap is excluded for a stronger reason: the
// version on file is not the one the question is about. agent_version is
// whatever the LAST connection reported, and in recovery that is the agent
// baked into boot.img, not the one installed on the node's disk — a rebake is
// netboot parity, never a release. Comparing it to the newest CDN release
// compares two different things, and update_agent is managed-OS-only, so the
// command it invites would sit registered until the node boots its OS. Both
// halves show together: the baked version read as "behind", and a queued
// update with zero delivery attempts.
func agentUpdate(n *node.Node, latest string) *Item {
	if latest == "" || n.IsWatcher() || n.IsRecoveryBootstrap() {
		return nil
	}
	current := n.AgentVersion
	if current == "" || current == "latest" {
		// An agent that never reported a version, or reported the alias. Not
		// evidence of being behind, and a false "update available" on every
		// such node would bury the real ones.
		return nil
	}
	if !node.AgentVersionLess(current, latest) {
		return nil
	}
	return &Item{Current: current, Expected: latest}
}

// bootloaderUpdate answers whether the device's EEPROM firmware is behind the
// signer's base, or nil when it cannot be told.
func bootloaderUpdate(n *node.Node, base map[string]string) *Item {
	if len(base) == 0 {
		return nil
	}
	rev, ok := n.BoardRevision()
	if !ok || rev.SoC == "" {
		// An unrecognised board must yield nothing rather than be compared
		// against another family's firmware, which reads as a false
		// "update available" on hardware that is current.
		return nil
	}
	expected := base[rev.SoC]
	if expected == "" || n.BootloaderVersion == "" {
		return nil
	}
	if node.BootloaderMatchesBase(n.BootloaderVersion, expected) {
		return nil
	}
	return &Item{Current: n.BootloaderVersion, Expected: expected}
}
