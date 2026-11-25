package node_command

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// NodeCommandStatus represents the status of a node command
type NodeCommandStatus string

const (
	StatusRegistered NodeCommandStatus = "registered"  // Command registered, waiting to be picked up by gRPC server
	StatusSent       NodeCommandStatus = "sent"        // Command sent to agent via gRPC
	StatusInProgress NodeCommandStatus = "in_progress" // Command execution in progress
	StatusSuccess    NodeCommandStatus = "success"     // Command completed successfully
	StatusFailed     NodeCommandStatus = "failed"      // Command failed
	// StatusSuperseded marks a queued command replaced by a newer one for the
	// same supersede key before it was ever sent. Kept distinct from failed so a
	// deliberate replacement is not read as an error.
	StatusSuperseded NodeCommandStatus = "superseded"
	// StatusExpired marks a queued command whose expires_at passed before the
	// node reconnected. Also distinct from failed.
	StatusExpired NodeCommandStatus = "expired"
	// StatusSkipped marks a command not applied by a deliberate precondition —
	// e.g. the node reconnected on an agent too old for the command. Distinct
	// from failed so a rollback-to-old-agent does not read as a real failure.
	StatusSkipped NodeCommandStatus = "skipped"
)

// String returns the string representation of the status
func (s NodeCommandStatus) String() string {
	return string(s)
}

// IsActive reports whether the command may still act on the device: it is
// registered (possibly queued for an offline node), sent, or executing. The
// five other statuses are terminal — success, failed, and the three that are
// deliberately not failures (superseded, expired, skipped).
//
// Stated as a method because "is this command still live?" is a question the
// device-safety paths ask, and a hand-rolled status list is one status short
// the day a sixth terminal state is added.
func (s NodeCommandStatus) IsActive() bool {
	switch s {
	case StatusRegistered, StatusSent, StatusInProgress:
		return true
	default:
		return false
	}
}

// Command slug constants
const (
	SlugReboot              = "reboot"
	SlugShutdown            = "shutdown"
	SlugStatusCheck         = "status_check"
	SlugInstallOS           = "install_os"
	SlugInstallAgent        = "install_agent"
	SlugUpdateBootOrder     = "update_boot_order"
	SlugUpdateFile          = "update_file"
	SlugProvisionConnection = "provision_connection"
	SlugEnableEEPROM        = "enable_eeprom"
	SlugEraseDrive          = "erase_drive"
	SlugUpdateAgent         = "update_agent"
	SlugSetHostname         = "set_hostname"
	SlugPatchFile           = "patch_file"
	SlugReadFile            = "read_file"
	SlugWaitForFile         = "wait_for_file"
	SlugRunScript           = "run_script"
	SlugApplyAccessConfig   = "apply_access_config"
	// SlugApplyAccessConfigOffline writes the SAME resolved access config as
	// apply_access_config, but into a mounted target disk from the netboot
	// recovery bootstrap — the path by which OU-assigned credentials reach a
	// node whose live-OS agent never runs. It is the managed-OS
	// hold's mirror image: meaningful ONLY in recovery, refused everywhere
	// else, and deliberately NOT queueable — "write this disk now, while the
	// node is netbooted" applied hours later against whatever the node boots
	// next is not the same command.
	SlugApplyAccessConfigOffline = "apply_access_config_offline"
	// SlugPatchFileOffline makes the SAME managed-block edit as patch_file, but
	// on the INSTALLED system's disk while the node runs the netboot recovery
	// bootstrap.
	//
	// It exists because the state in which an operator most needs to edit
	// /boot/firmware/config.txt — a node whose installed OS will not boot — is
	// precisely the state in which patch_file cannot reach it: from the
	// initramfs that path is the BOOTSTRAP's own boot partition, so the live
	// command would edit the wrong file and report success. Not queueable, for
	// apply_access_config_offline's reason: "write this disk now, while the node
	// is netbooted" applied hours later against whatever the node boots next is
	// a different command.
	SlugPatchFileOffline    = "patch_file_offline"
	SlugCaptureImage        = "capture_image"
	SlugInstallPackages     = "install_packages"
	SlugManageService       = "manage_service"
	SlugDownloadFile        = "download_file"
	SlugWriteFile           = "write_file"
	SlugDeleteFile          = "delete_file"
	SlugApplyWatchdogConfig = "apply_watchdog_config"
	// SlugReleaseIdentity tells a node to delete its own adoption file so the
	// device returns to a clean unadopted state. Dispatched when the platform
	// deletes the node — see the force gate in node.Service.Delete.
	SlugReleaseIdentity = "release_identity"
	// SlugApplyDeviceProfile writes the device-profile marker block into
	// config.txt and runs the equipage's typed setup steps as ONE command with
	// one terminal status. Deliberately NOT patch_file + run_script: the two
	// have opposite queueability (an offline node would get half an apply), and
	// patch_file's supersede key is slug-scoped, so a queued profile apply and
	// a queued overclock apply would silently retire each other.
	SlugApplyDeviceProfile = "apply_device_profile"
	// SlugIdentify blinks the node's locate LED so a human can find the
	// physical machine. The agent reports terminal success immediately after
	// STARTING the blink (the auto-stop timer is agent-local) — otherwise the
	// exact node an operator is trying to find would sit in `maintenance` for
	// the whole blink duration.
	SlugIdentify = "identify"
	// SlugApplyUtilityConfig declaratively applies the node's resolved utility
	// set (docs/UTILITIES-PLAN.md) as ONE wholesale command with one terminal
	// status. Wholesale on purpose: per-utility dispatches would share this
	// slug's supersede key and retire each other — the defect
	// apply_device_profile's own slug exists to avoid — while one wholesale
	// set gets queueing, supersede (a newer set retires an older unsent one),
	// TTL and redelivery-after-recovery for free.
	SlugApplyUtilityConfig = "apply_utility_config"

	// SlugApplyLEDConfig declaratively converges the node's LED behaviour:
	// stealth mode (blade transistor), ACT/PWR sysfs disable and the per-LED
	// ambient indication. Wholesale like every other apply_* — the payload IS
	// the desired state — and persisted agent-side for boot re-apply.
	SlugApplyLEDConfig = "apply_led_config"
)

// MaxDelaySeconds bounds reboot/shutdown delay_seconds. The agent executes the
// delay as a time.Sleep on its single command goroutine, so an unbounded value
// parks the node's command handling for as long as the caller asks — and the
// proto field is a uint32, so a negative value would wrap into a sleep of
// years. An hour is far above any legitimate staged reboot and far below
// "indefinitely". The bound is enforced by a dispatch-time clamp
// (ClampDelaySeconds), not save-time validation, so it also covers playbooks
// saved before it existed — the same shape as the http_request timeout ceiling.
const MaxDelaySeconds = 3600

// ClampDelaySeconds bounds a delay_seconds value to [0, MaxDelaySeconds] and
// reports whether the clamp actually bound, so the caller can log it.
func ClampDelaySeconds(v float64) (clamped float64, bound bool) {
	switch {
	case v < 0:
		return 0, true
	case v > MaxDelaySeconds:
		return MaxDelaySeconds, true
	default:
		return v, false
	}
}

// MaxBlockSeconds bounds the workflow blocks that WAIT on the agent:
// wait_for_file's poll interval and timeout, and run_script's timeout. Same
// ceiling and same reasoning as MaxDelaySeconds — the agent runs all of them on
// its single command goroutine, so an unbounded value parks that node's command
// handling (and its terminal, which shares the read loop).
//
// It is enforced at the PROTO BUILD rather than at save time, like the
// http_request ceiling, so it also covers playbooks saved before it existed.
// And it must clamp the float64 BEFORE the int32 conversion: that conversion is
// architecture-dependent for an out-of-range value — arm64 saturates to
// 2147483647 (a 68-year sleep), amd64 wraps negative — so an unclamped payload
// wedges a node when the server runs on arm64 and does nothing on amd64.
const MaxBlockSeconds = 3600

// ClampBlockSeconds bounds a block's wait to [0, MaxBlockSeconds] and reports
// whether the clamp bound, so the caller can log it. Zero is left alone: each
// handler has its own default for "unset".
func ClampBlockSeconds(v float64) (clamped float64, bound bool) {
	switch {
	case v < 0:
		return 0, true
	case v > MaxBlockSeconds:
		return MaxBlockSeconds, true
	default:
		return v, false
	}
}

// queueableSlugs are the commands safe to hold in the queue for an offline node
// and deliver on reconnect: declarative and idempotent, where "apply later"
// still means the same thing. Imperative or time-sensitive commands are NOT
// queueable — they fail fast instead of silently applying minutes or hours late
// (reboot/shutdown mean "now"; install_os carries a signed URL that expires in
// an hour; run_script/read_file are point-in-time requests).
var queueableSlugs = map[string]bool{
	SlugApplyAccessConfig:   true,
	SlugSetHostname:         true,
	SlugPatchFile:           true,
	SlugApplyWatchdogConfig: true,
	SlugApplyDeviceProfile:  true,
	SlugApplyUtilityConfig:  true,
	SlugApplyLEDConfig:      true,
	// An agent update is a desired STATE ("run this version"), not a moment,
	// and it was the one such command that failed instantly against an offline
	// node — so a node that was dark when the fleet was updated stayed on the
	// old agent with nothing recording that it had missed one. Queued, it is
	// bounded by the same 24-hour TTL as the rest, superseded by a newer
	// update for the same node, and re-checked against the agent-version floor
	// at REDELIVERY, so it cannot land on an agent that was downgraded while it
	// waited.
	//
	// It is held while netbooted (managedOSOnlySlugs below): the recovery
	// initramfs has no dpkg and no persistent root.
	SlugUpdateAgent: true,
}

// IsQueueable reports whether a command may wait in the queue for an offline
// node instead of failing immediately.
func IsQueueable(slug string) bool {
	return queueableSlugs[slug]
}

// managedOSOnlySlugs are queueable commands that are meaningless on the netboot
// recovery bootstrap (a Buildroot initramfs: no groupadd/usermod, no persistent
// root filesystem) and would only produce a spurious terminal FAILED there.
// They are held queued while the node reports current_boot_device == "http" and
// delivered once a fresh system-info report shows a managed OS.
var managedOSOnlySlugs = map[string]bool{
	SlugApplyAccessConfig: true,
	// The recovery initramfs has no systemd units to arm and no persistent
	// root, so arming there could only report a spurious failure.
	SlugApplyWatchdogConfig: true,
	// The recovery initramfs has no /boot/firmware and no services to
	// configure; worse, an apply "succeeding" there would record an applied
	// hash for a config.txt that evaporates on the next reboot.
	SlugApplyDeviceProfile: true,
	// The recovery initramfs has no apt, no systemd and no persistent root —
	// installing software there could only record state that evaporates.
	SlugApplyUtilityConfig: true,
	// The persisted-state half of the LED apply lives on the installed OS
	// (state file + systemd-started agent); applying it to the initramfs
	// records state that evaporates on the next boot. Visible consequence,
	// accepted deliberately: a stealth-mode blade LIGHTS UP while netbooted
	// (nothing re-applies the config there) and goes dark again when its OS
	// returns — see docs/DEVICE-PROFILES.md, "LED control and the monolith
	// decision".
	SlugApplyLEDConfig: true,
	// The recovery initramfs has no dpkg, no systemd unit to restart and no
	// persistent root: installing the agent there could only report a success
	// that evaporates on the next boot. Note the asymmetry with install_agent,
	// which is a RECOVERY operation onto a mounted volume and is not queueable
	// at all.
	SlugUpdateAgent: true,
}

// RequiresManagedOS reports whether a queued command must wait for the node to
// be booted into a managed OS (not the recovery bootstrap) before delivery.
func RequiresManagedOS(slug string) bool {
	return managedOSOnlySlugs[slug]
}

// rebootingSlugs are the commands that END by rebooting the device, so the node
// must be pinned to "restarting" at DISPATCH rather than trusted to report it.
//
// The agent cannot be relied on here, and update_boot_order shows why better
// than reboot does: it reports the command SUCCEEDED, which returns the node to
// online, and only then sends a "restarting" status two seconds before the
// machine goes down. The platform loses that race and shows a rebooting device
// as ONLINE — observed on a CM4 flashing its EEPROM.
//
// Pinning at dispatch is what the stale-connection monitor preserves through
// the downtime, which is the same reasoning the reboot slug already carried.
// Membership is a claim that the command MIGHT take the node down, not that it
// will: the pin is pessimistic on purpose and is released the moment the agent
// says otherwise (see AgentDeclinedReboot). Measured across repeated flashes:
// update_boot_order does NOT reboot on the path that has rpi-eeprom-update — it answers "Signed EEPROM image flashed
// successfully. Reboot required for the new boot order to take effect", and
// last_boot_at never moved. Its OTHER path, the recovery package staged for the
// ROM, really does reboot, which is why the slug is here at all.
//
// Through agent 0.7.8 the slug sent no result map, so the pin was released only
// by the agent's own follow-up "online" status report and lasted about thirteen
// seconds. An agent that declares the branch it took (both branches now set
// RebootingResultKey) releases it with the success report instead. Nothing here
// changes for an older one: absence still means unknown, and unknown still
// keeps the pin, because the alternative is trusting an agent about a reboot it
// has not performed yet.
//
// A pin therefore says "a command that MIGHT restart this node is running", and
// nodes.pinned_by_slug says WHICH — see node.Node.PinnedBySlug. The durable
// "flashed, reboot still owed" fact is nodes.bootloader_flashed_at (the settle
// hook), not this pin.
var rebootingSlugs = map[string]bool{
	SlugReboot: true,
	// Stages a signed EEPROM package for the ROM to apply on the next boot.
	// Held here for the window before the agent reports, not because the flash
	// itself restarts anything.
	SlugUpdateBootOrder: true,
	// Writes the CM4 SPI settings into config.txt, which only take effect after
	// a boot, and reboots for them.
	SlugEnableEEPROM: true,
}

// CausesReboot reports whether dispatching this command will take the node down
// for a reboot.
func CausesReboot(slug string) bool {
	return rebootingSlugs[slug]
}

// nonDisruptiveSlugs take the node out of NOTHING: they change no state on the
// device, cannot fail it into an odd condition, and finish in well under a
// second. Every other command pins the node to "maintenance" while it runs, and
// for those that is the point — an operator must see that a machine is being
// worked on.
//
// Identify is the counter-example that earned this set. Pressing ID blinks an
// LED, and pinning the node to "maintenance" for it made every list watching
// the WebSocket flash the node out of "online" and back within a few hundred
// milliseconds: a status change that says something happened, over an action
// that did nothing to the node. Worse, the pin outlives the blink if the
// terminal report is ever lost — the command then sits in_progress until the
// abandonment sweep, and the node reads as under maintenance for as long.
//
// Adding a slug here is a claim that BOTH halves hold: it changes nothing on
// the device, AND nobody needs to be warned it is running. A scan does not
// qualify — it runs a script on the machine, and the maintenance pin is how a
// second operator learns not to start something else.
var nonDisruptiveSlugs = map[string]bool{
	SlugIdentify: true,
}

// IsNonDisruptive reports whether this command may run without moving the
// node's status at all.
func IsNonDisruptive(slug string) bool {
	return nonDisruptiveSlugs[slug]
}

// NonDisruptiveSlugs lists them for the repository query that decides whether a
// node is still busy. Sorted, so the generated SQL is stable.
func NonDisruptiveSlugs() []string {
	slugs := make([]string, 0, len(nonDisruptiveSlugs))
	for slug := range nonDisruptiveSlugs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

// deviceExclusiveSlugs are the commands that take a block device for
// themselves. At most ONE of them may be in flight on a node at a time, and the
// set is exclusive as a GROUP rather than per slug: what must not happen is two
// writers on the same disk, and `erase_drive` racing `install_os` is exactly as
// destructive as two installs.
//
// The window is the whole download. Two `install_os` runs can hold the same
// block device open O_WRONLY and interleave writes at their own offsets, and
// the agent's own validation does not close that window: it refuses a device
// that "has partitions", but the agent holds the first 4 MiB of the image —
// protective MBR, GPT header, partition entries — in RAM until the digest
// matches, so for the ENTIRE download the target carries no partition table
// at all and a second dispatch passes validation.
//
// Deliberately NOT a node-wide "one command at a time" lock. Commands are
// accepted for a node in `maintenance` on purpose — that is how a stuck node is
// recovered — and a node-wide lock would also block the `install_agent` and
// `reboot` that legitimately follow an install.
var deviceExclusiveSlugs = map[string]bool{
	SlugInstallOS:    true,
	SlugEraseDrive:   true,
	SlugCaptureImage: true,
	// The offline access apply mounts the target disk and writes into its
	// rootfs; racing an install/erase/capture on the same device would write
	// users into a filesystem that is being overwritten under it.
	SlugApplyAccessConfigOffline: true,
	// The offline patch mounts a partition of the target disk; racing an
	// install/erase/capture on the same device would edit a filesystem being
	// overwritten under it.
	SlugPatchFileOffline: true,
}

// IsDeviceExclusive reports whether this command owns a block device for its
// duration.
func IsDeviceExclusive(slug string) bool {
	return deviceExclusiveSlugs[slug]
}

// DeviceExclusiveSlugs returns the exclusive set, for a repository query that
// has to ask the database about all of them at once.
func DeviceExclusiveSlugs() []string {
	out := make([]string, 0, len(deviceExclusiveSlugs))
	for slug := range deviceExclusiveSlugs {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// RebootingResultKey is the result field an agent sets to say whether a
// reboot-class command is actually going to reboot THIS device.
//
// It exists because enable_eeprom only reboots on a CM4: the SPI overlays it
// writes live in config.txt's [cm4] section, which never matches a CM5 or a
// Pi 5, so on every other board the agent deliberately finishes without
// rebooting. The platform pinned the node to "restarting" at dispatch anyway
// and then waited for a reconnect that was never coming.
const RebootingResultKey = "rebooting"

// AgentDeclinedReboot reports whether the agent EXPLICITLY said it is not
// rebooting. Absence means "unknown", not "no": an agent that predates this
// field must keep the dispatch-time pin, which is the whole reason the pin does
// not trust the agent in the first place.
func AgentDeclinedReboot(result map[string]string) bool {
	return result != nil && result[RebootingResultKey] == "false"
}

// minAgentVersion is the lowest agent version that understands each command.
// The gate is fail-closed: an unknown or unparseable agent version is treated
// as too old (see CompareSemver / MeetsMinAgentVersion). Versions live here
// rather than only in docs so the check actually runs.
var minAgentVersion = map[string]string{
	// Raised from 0.3.0 to 0.5.0 when the platform stopped sending cleartext
	// passwords: the password now travels as a
	// crypt(3) `$6$` hash in AccessUser.password_hash and the old `password`
	// field is left empty. An agent below the floor reads only the old field,
	// which means it would apply the config with NO password — and "no password"
	// is not a smaller version of the intent, it is the opposite one: it turns a
	// sudo user who should be prompted into NOPASSWD. Refusing the whole command
	// leaves the device on its last applied config, which is the safe side.
	//
	// The cost, stated because it is real: a node that stays on an older agent
	// stops receiving access-config changes entirely, and its queued applies
	// settle `skipped`. The remedy is update_agent.
	SlugApplyAccessConfig: "0.5.0",
	// provision_connection is the second cleartext-password sender and got the
	// same treatment: the password now travels as a `$6$` hash in
	// password_hash. It gained a floor where it had none, because an older agent
	// reads only the retired `password` field and would create the node's first
	// OS user with NO password — the one account the operator is counting on to
	// get into a freshly provisioned device.
	SlugProvisionConnection: "0.5.0",
	SlugCaptureImage:        "0.3.0",
	SlugInstallPackages:     "0.3.0",
	SlugManageService:       "0.3.0",
	SlugDownloadFile:        "0.3.0",
	SlugWriteFile:           "0.3.0",
	// delete_file lands in the same agent release as the watchdog command, so
	// the floor is that release's number. The check is fail-closed: an agent
	// that predates it is refused rather than sent a command it will drop.
	SlugDeleteFile: "0.4.0",
	// The watchdog command, the liveness ticker and the on-device breaker are
	// one indivisible agent release; the floor keeps it inert against every
	// agent that predates them.
	SlugApplyWatchdogConfig: "0.4.0",
	// read_file predates this floor, but the bounded, symlink-safe, UTF-8
	// checked implementation does not. An older agent runs a bare os.ReadFile:
	// a large file overruns the gRPC frame and tears down the node's stream, a
	// binary one fails the proto marshal with the same result, and a FIFO
	// blocks the agent's single command loop until someone restarts it. None of
	// that is catchable on the platform, because it happens after dispatch and
	// before any reply.
	SlugReadFile: "0.4.0",
	// wait_for_file has exactly read_file's problem and never had its floor.
	// Before the bounded reader it called a bare os.ReadFile INSIDE its poll
	// loop, so every hazard read_file was gated for repeats every interval for
	// up to ten minutes: a FIFO blocks the agent's single command goroutine, a
	// large file overruns the gRPC frame and tears down the node's stream, a
	// binary one fails the proto marshal the same way. It also skipped the read
	// policy entirely until the same release, and it returns a regex capture
	// group — which is all it takes to read the adoption file's unique_token off
	// a device.
	SlugWaitForFile: "0.4.0",
	// release_identity is what makes deleting a node leave the device in a clean
	// state instead of stranded. Fail-closed matters MORE here than elsewhere:
	// an older agent would ignore the command, the delete would proceed, and the
	// operator would be told the device was released when it was not. Better to
	// refuse the release and fall back on the deleted-node record.
	SlugReleaseIdentity: "0.5.0",
	// The device-profile apply and identify ship in the same agent release as
	// their handlers; an older agent decodes neither oneof arm and would report
	// "unknown command type" as a plain failure. A queued apply against an
	// older agent settles `skipped`, which the profile UI surfaces as the
	// remedy being update_agent.
	SlugApplyDeviceProfile: "0.6.0",
	SlugIdentify:           "0.6.0",
	// The offline access apply ships with the agent release that carries its
	// handler; an older agent decodes neither the oneof arm nor the mount
	// machinery and would report "unknown command type". Fail-closed as
	// everywhere: an empty or unparseable agent version is refused.
	SlugApplyAccessConfigOffline: "0.6.2",
	// The offline patch ships in 0.6.4. The floor is fail-closed by
	// construction (empty/unparseable counts as too old), which matters more
	// here than usual: dispatched to an older RECOVERY agent the command is
	// simply unknown, and an unknown command is the one failure an operator
	// cannot act on from the message.
	SlugPatchFileOffline: "0.6.4",
	// The utility apply ships in the agent release that carries its handler; an
	// older agent decodes neither the oneof arm nor the per-utility appliers
	// and would report "unknown command type". Fail-closed as everywhere; a
	// queued apply against an older agent settles `skipped` and the UI names
	// update_agent as the remedy.
	SlugApplyUtilityConfig: "0.6.5",
	// The LED apply ships in the agent release that carries its handler, the
	// stealth GPIO line and the ambient indication loop; an older agent
	// decodes neither the oneof arm nor the manager. Fail-closed as
	// everywhere; a queued apply against an older agent settles `skipped`.
	SlugApplyLEDConfig: "0.6.8",
	// run_script's floor is the release that added handlers.MaxResultOutputBytes
	// (128 KiB per output value) on the agent side. Below it a script's stdout
	// travels unbounded: a result over the frame ceiling tears down the node's
	// gRPC stream, loses the report, leaves the node_command in_progress until
	// the abandonment sweep and the execution short of completed_at — silently
	// consuming a per-account active-execution slot. The floor is deliberately
	// low: its job is to make the fail-closed comparison run for the most
	// consequential slug there is, not to exclude versions. It must never sit
	// above the oldest agent version still expected to connect: every node
	// below a floor is refused that slug outright, so raising it past a live
	// version takes run_script — and every playbook built on it — away from
	// those devices until each is updated.
	SlugRunScript: "0.4.6",
	// patch_file's floor is the release that INTRODUCED it, deliberately low,
	// and, as for run_script, never above the oldest agent version still
	// expected to connect — the point is not to exclude versions but to
	// make the fail-closed comparison run at all: an agent reporting an empty,
	// "latest" or unparseable version is refused a queueable file rewrite
	// instead of receiving it. Raising it to the hardened handler's release
	// (0.6.0) was considered and rejected: it would break overclock for devices
	// on older 0.5.x agents, and the hardening it would buy guards only against
	// the account's own privileged operator.
	SlugPatchFile: "0.2.16",
}

// MinAgentVersion returns the minimum agent version for a slug and whether one
// is required. Commands with no entry impose no version floor.
func MinAgentVersion(slug string) (string, bool) {
	v, ok := minAgentVersion[slug]
	return v, ok
}

// MinAgentVersionStripIdentity is the lowest agent version whose install_os
// understands the strip_identity flag (deploying a backup image onto a node
// other than its source). Enforced conditionally at proto-build time — plain
// installs keep working on older agents.
const MinAgentVersionStripIdentity = "0.3.3"

// MinAgentVersionWatcherInstall is the lowest RECOVERY agent whose
// install_agent understands `variant: "watcher"`. Enforced conditionally, like
// strip_identity: a plain full-agent install keeps working on older recovery
// agents, but variant=watcher is REFUSED below the floor. Two generations of
// reason: below 0.6.3 the agent ignores the unknown FIELD and silently
// installs the FULL agent; 0.6.3–0.6.x understand the field but only its
// pre-rename value "observer", so the renamed value would fail with "unknown
// agent variant" mid-install. 0.7.0 is the release that renamed the wire
// value — met by the agent baked into the SAME release's boot.img.
const MinAgentVersionWatcherInstall = "0.7.0"

// AgentVariantFull / AgentVariantWatcher are the install_agent `variant`
// values. Product name and wire value agree since the 0.7.0 full rename; the
// retired wire value "observer" must not be reused.
const (
	AgentVariantFull    = "full"
	AgentVariantWatcher = "watcher"
)

// requiresPersistentRootSlugs are the commands whose whole point is a WRITE
// that must survive a reboot: package installs, users and keys, hostname,
// config files, watchdog/LED/profile/utility applies. On a root filesystem
// running an overlay (overlayroot: upper layer in tmpfs) each of these lands,
// reads back as applied — the recorded generation then SUPPRESSES re-apply —
// and evaporates at the next boot. A key REVOCATION that evaporates is the
// worst member of the family, which is why the gate refuses rather than
// warns.
//
// Deliberately absent: run_script (diagnostic reads — the OS-user scan rides
// it; a script's own writes are the operator's choice), reboot/shutdown/
// identify/status_check (no persistent write), the EEPROM pair (they write
// the EEPROM, which no overlay covers), erase_drive/install_os/install_agent/
// capture_image (they address a DISK, not the running root), the *_offline
// pair (they write into a mounted target from recovery), read/wait/download
// (reads), release_identity (its write is best-effort by design).
var requiresPersistentRootSlugs = map[string]bool{
	SlugUpdateAgent:         true,
	SlugApplyAccessConfig:   true,
	SlugProvisionConnection: true,
	SlugSetHostname:         true,
	SlugPatchFile:           true,
	SlugUpdateFile:          true,
	SlugWriteFile:           true,
	SlugDeleteFile:          true,
	SlugInstallPackages:     true,
	SlugManageService:       true,
	SlugApplyWatchdogConfig: true,
	SlugApplyDeviceProfile:  true,
	SlugApplyUtilityConfig:  true,
	SlugApplyLEDConfig:      true,
}

// RequiresPersistentRoot reports whether the slug's effect must outlive a
// reboot and is therefore refused while the node's root filesystem is an
// overlay.
func RequiresPersistentRoot(slug string) bool {
	return requiresPersistentRootSlugs[slug]
}

// ResolveSlug maps an inbound command name to its canonical slug. Only "restart"
// differs from its slug; everything else is already canonical. Callers need the
// slug before building a payload so queueability and the version floor can be
// decided up front.
func ResolveSlug(command string) string {
	if command == "restart" {
		return SlugReboot
	}
	return command
}

// defaultQueueTTL bounds how long a queued command may wait for an offline
// node. A revocation redelivered to a node that has been dark for weeks is
// applying intent that may itself have been superseded several times over; past
// this point the operator should re-push against current state instead.
const defaultQueueTTL = 24 * time.Hour

// queueTTL allows a per-slug override of defaultQueueTTL.
var queueTTL = map[string]time.Duration{}

// QueueTTL returns how long a queued command of this slug stays deliverable.
func QueueTTL(slug string) time.Duration {
	if d, ok := queueTTL[slug]; ok {
		return d
	}
	return defaultQueueTTL
}

// MaxDeliveryAttempts caps redelivery so a command that cannot be delivered
// (node reconnect-loops, agent rejects it every time) fails visibly instead of
// being retried on every reconnect forever.
const MaxDeliveryAttempts = 5

// SupersedeKeyFor builds the key that lets a newer command replace an older,
// still-unsent one for the same node and command type.
func SupersedeKeyFor(slug string, nodeID uuid.UUID) string {
	return slug + ":" + nodeID.String()
}

// NodeCommand represents a command sent to a node
// WorkflowLink is the projection the workflow engine needs when a device
// command completes: who owns the command, and which run (if any) is waiting on
// it. It exists so that path does not have to load — and lock — the whole
// command row on every reboot, scan and install the fleet performs.
type WorkflowLink struct {
	AccountID uuid.UUID
	NodeID    uuid.UUID
	// ExecutionNodeID is nil when the command belongs to no workflow run.
	ExecutionNodeID *uuid.UUID
}

type NodeCommand struct {
	ID        uuid.UUID
	NodeID    uuid.UUID
	AccountID uuid.UUID
	Slug      string                 // Command identifier like "reboot", "install_os", "install_agent", etc.
	Payload   map[string]interface{} // Command parameters as JSON
	Status    NodeCommandStatus
	Progress  int    // 0-100, optional progress percentage
	Details   string // Optional additional status information (error messages, logs, etc.)
	// Result carries structured outputs reported by the agent (read_file
	// content, run_script exit_code/stdout/stderr, ...) for workflow blocks.
	Result map[string]string
	// Queue metadata. Queueable marks a command that may wait for an offline
	// node; SupersedeKey (e.g. "apply_access_config:<nodeID>") lets a newer
	// command replace an unsent one; ExpiresAt bounds the wait; Attempts counts
	// delivery tries.
	Queueable    bool
	SupersedeKey string
	ExpiresAt    *time.Time
	Attempts     int
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// UndecryptablePayloadKeys names the payload fields that could not be read
	// back — the key that sealed them is gone. Populated on READ and persisted
	// nowhere.
	//
	// It exists because the read path DELETES what it cannot decrypt, which is
	// right for display (ciphertext tells an operator nothing) and catastrophic
	// for dispatch: a queued apply_access_config whose users or keys silently
	// vanished is sent as a SMALLER config and reports success, which is how a
	// revocation turns into a grant. Dispatch refuses a command carrying any of
	// these; nothing else has to remember to check, because the refusal sits in
	// the one function that turns a stored command into a wire command.
	UndecryptablePayloadKeys []string

	// WorkflowExecutionNodeID is the run step that dispatched the command, nil
	// for a command a member sent directly. Populated on READ; the link is
	// written through the repository's own parameter, never from here.
	WorkflowExecutionNodeID *uuid.UUID
}

// PayloadIsComplete reports whether every stored payload field survived
// decryption. False means the command must not be dispatched.
func (c *NodeCommand) PayloadIsComplete() bool {
	return len(c.UndecryptablePayloadKeys) == 0
}

// allStatuses is every value NodeCommandStatus takes. It exists so a filter
// can refuse an unrecognised status instead of ignoring it — an ignored filter
// returns everything, which a caller reads as "everything matched".
var allStatuses = []NodeCommandStatus{
	StatusRegistered,
	StatusSent,
	StatusInProgress,
	StatusSuccess,
	StatusFailed,
	StatusSuperseded,
	StatusExpired,
	StatusSkipped,
}

// AllStatuses returns the command lifecycle in order, oldest state first.
func AllStatuses() []NodeCommandStatus {
	out := make([]NodeCommandStatus, len(allStatuses))
	copy(out, allStatuses)
	return out
}

// IsKnownStatus reports whether s names a real command status.
func IsKnownStatus(s string) bool {
	for _, known := range allStatuses {
		if string(known) == s {
			return true
		}
	}
	return false
}
