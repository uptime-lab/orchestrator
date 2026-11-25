package node_attention

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

var now = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// connected is a node that trips none of the reachability checks, so a test
// can add exactly one condition and assert exactly one item.
func connected() *node.Node {
	seen := now.Add(-time.Minute)
	return &node.Node{
		ID:         uuid.New(),
		Name:       "node",
		Status:     node.StatusOnline,
		CreatedAt:  now.Add(-30 * 24 * time.Hour),
		LastSeenAt: &seen,
	}
}

func kinds(items []Item) []Kind {
	out := make([]Kind, 0, len(items))
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return out
}

func has(items []Item, k Kind) *Item {
	for i := range items {
		if items[i].Kind == k {
			return &items[i]
		}
	}
	return nil
}

// The four reachability answers are mutually exclusive. Listing a node as both
// "offline" and "never came back after install_os" makes the list longer
// without telling an operator anything more.
func TestReachabilityReportsOneAnswer(t *testing.T) {
	cases := []struct {
		name string
		node func() *node.Node
		want Kind
	}{
		{
			name: "awaiting return beats offline",
			node: func() *node.Node {
				n := connected()
				n.Status = node.StatusOffline
				since := now.Add(-time.Hour)
				n.AwaitingReturnAfter = "install_os"
				n.AwaitingReturnSince = &since
				return n
			},
			want: KindAwaitingReturn,
		},
		{
			name: "pending approval beats everything",
			node: func() *node.Node {
				n := connected()
				n.Status = node.StatusOffline
				n.ApprovalStatus = node.ApprovalStatusPendingApproval
				n.AwaitingReturnAfter = "install_os"
				return n
			},
			want: KindPendingApproval,
		},
		{
			name: "never seen",
			node: func() *node.Node {
				n := connected()
				n.Status = node.StatusOffline
				n.LastSeenAt = nil
				return n
			},
			want: KindNeverConnected,
		},
		{
			name: "plain offline",
			node: func() *node.Node {
				n := connected()
				n.Status = node.StatusOffline
				return n
			},
			want: KindOffline,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := Evaluate(tc.node(), Facts{Now: now})
			if got := kinds(items); len(got) != 1 || got[0] != tc.want {
				t.Errorf("kinds = %v, want exactly [%s]", got, tc.want)
			}
		})
	}
}

// A node adopted minutes ago has not failed to connect yet. A row that appears
// and disappears on its own trains an operator to ignore the list.
func TestFreshlyAdoptedNodeIsNotReportedAsNeverConnected(t *testing.T) {
	n := connected()
	n.Status = node.StatusOffline
	n.LastSeenAt = nil
	n.CreatedAt = now.Add(-5 * time.Minute)
	if items := Evaluate(n, Facts{Now: now}); len(items) != 0 {
		t.Errorf("kinds = %v, want none", kinds(items))
	}
}

// The whole point of Facts: a check whose input is missing must be silent, not
// reassuring. An unreadable release listing that answered "up to date" would
// show a fleet as current while every node on it is behind.
func TestAgentCheckIsSilentWhenTheLatestReleaseIsUnknown(t *testing.T) {
	n := connected()
	n.AgentVersion = "0.6.1"
	if items := Evaluate(n, Facts{Now: now}); len(items) != 0 {
		t.Errorf("kinds = %v, want none when LatestAgentVersion is empty", kinds(items))
	}
}

func TestAgentUpdate(t *testing.T) {
	cases := []struct {
		name    string
		version string
		kind    string
		latest  string
		want    bool
	}{
		{name: "behind", version: "0.6.9", latest: "0.6.13", want: true},
		// A numeric-aware comparison, or 0.6.9 reads as newer than 0.6.13.
		{name: "current", version: "0.6.13", latest: "0.6.13", want: false},
		{name: "ahead of the CDN", version: "0.7.0", latest: "0.6.13", want: false},
		{name: "unreported version", version: "", latest: "0.6.13", want: false},
		{name: "the latest alias", version: "latest", latest: "0.6.13", want: false},
		// A watcher executes no commands, so update_agent can never reach it.
		// Offering the update would be an action the operator cannot take.
		{name: "watcher", version: "0.6.9", kind: node.AgentKindWatcher, latest: "0.6.13", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := connected()
			n.AgentVersion = tc.version
			n.AgentKind = tc.kind
			items := Evaluate(n, Facts{Now: now, LatestAgentVersion: tc.latest})
			got := has(items, KindAgentOutdated)
			if (got != nil) != tc.want {
				t.Fatalf("agent_outdated present = %v, want %v (kinds %v)", got != nil, tc.want, kinds(items))
			}
			if got != nil && (got.Current != tc.version || got.Expected != tc.latest) {
				t.Errorf("versions = %q/%q, want %q/%q", got.Current, got.Expected, tc.version, tc.latest)
			}
		})
	}
}

// The signer reports a short hash and the agent the full one, so an equality
// test calls a current device outdated. An unrecognised board must yield
// nothing rather than be compared against the other family's firmware.
func TestBootloaderUpdate(t *testing.T) {
	const cm5Rev = "d04170" // BCM2712
	cases := []struct {
		name     string
		revision string
		reported string
		base     map[string]string
		want     bool
	}{
		{name: "short base matches long report", revision: cm5Rev, reported: "f68405bcabcdef", base: map[string]string{"cm5": "f68405bc"}},
		{name: "vcgencmd suffix still matches", revision: cm5Rev, reported: "f68405bc (release)", base: map[string]string{"cm5": "f68405bc"}},
		{name: "behind", revision: cm5Rev, reported: "aa11bb22", base: map[string]string{"cm5": "f68405bc"}, want: true},
		{name: "no base for this SoC", revision: cm5Rev, reported: "aa11bb22", base: map[string]string{"cm4": "f68405bc"}},
		{name: "unrecognised board", revision: "", reported: "aa11bb22", base: map[string]string{"cm5": "f68405bc"}},
		{name: "no signer", revision: cm5Rev, reported: "aa11bb22", base: nil},
		{name: "device reported nothing", revision: cm5Rev, reported: "", base: map[string]string{"cm5": "f68405bc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := connected()
			n.BootloaderVersion = tc.reported
			if tc.revision != "" {
				n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{
					CPU: &node.CPUInfo{BoardRevision: tc.revision},
				}}
			}
			items := Evaluate(n, Facts{Now: now, BaseFirmware: tc.base})
			if got := has(items, KindBootloaderOutdated); (got != nil) != tc.want {
				t.Errorf("bootloader_outdated present = %v, want %v (kinds %v)", got != nil, tc.want, kinds(items))
			}
		})
	}
}

// A disarm the platform asked for is not a device fault. Reporting it as one
// resurfaces every deliberate disarm as an alert that cannot be cleared.
func TestPlatformDisarmIsNotAnAttentionItem(t *testing.T) {
	n := connected()
	n.WatchdogDisarmReason = node.PlatformDisarmReason
	if items := Evaluate(n, Facts{Now: now}); len(items) != 0 {
		t.Errorf("kinds = %v, want none", kinds(items))
	}
	n.WatchdogDisarmReason = "three restarts in a row"
	items := Evaluate(n, Facts{Now: now})
	got := has(items, KindWatchdogDisarmed)
	if got == nil {
		t.Fatalf("kinds = %v, want watchdog_disarmed", kinds(items))
	}
	if got.Subject != "three restarts in a row" {
		t.Errorf("subject = %q, want the breaker's reason", got.Subject)
	}
}

// A nil KnownOUs is "the unit tree could not be read". Treating it as an empty
// set would declare every assigned node's unit missing at once — the loudest
// possible wrong answer, on the check whose real cases are unbootable devices.
func TestDanglingUnitNeedsAKnownUnitSet(t *testing.T) {
	ou := uuid.New()
	n := connected()
	n.OUID = &ou

	if items := Evaluate(n, Facts{Now: now}); len(items) != 0 {
		t.Errorf("kinds = %v, want none when the unit tree is unknown", kinds(items))
	}
	if items := Evaluate(n, Facts{Now: now, KnownOUs: map[uuid.UUID]bool{ou: true}}); len(items) != 0 {
		t.Errorf("kinds = %v, want none for a live unit", kinds(items))
	}
	items := Evaluate(n, Facts{Now: now, KnownOUs: map[uuid.UUID]bool{uuid.New(): true}})
	if has(items, KindDanglingOU) == nil {
		t.Errorf("kinds = %v, want dangling_ou", kinds(items))
	}
	if SeverityFor(KindDanglingOU) != SeverityCritical {
		t.Error("a node that can never boot again is not a warning")
	}
}

// A watcher runs no commands, so a boot order that cannot reach HTTP boot has
// taken away the only way back to it. It is checked at install time and, until
// this list, never again.
func TestWatcherThatCannotNetbootIsCritical(t *testing.T) {
	n := connected()
	n.AgentKind = node.AgentKindWatcher
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{
		Boot: &node.BootInfo{BootDevicesOrder: []string{"nvme", "sd"}},
	}}
	items := Evaluate(n, Facts{Now: now})
	got := has(items, KindWatcherUnrecoverable)
	if got == nil {
		t.Fatalf("kinds = %v, want watcher_unrecoverable", kinds(items))
	}
	if got.Subject == "" {
		t.Error("the row must carry the reason the boot order cannot return")
	}
	if got.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical", got.Severity)
	}

	// "network" is PXE, which boots nothing of ours, so the walk crosses it and
	// reaches http. A local-media entry ahead of http would not be crossed:
	// after an OS is installed the disk wins and the device never asks us again.
	n.SystemInfo.Hardware.Boot.BootDevicesOrder = []string{"network", "http", "nvme"}
	if items := Evaluate(n, Facts{Now: now}); has(items, KindWatcherUnrecoverable) != nil {
		t.Errorf("kinds = %v, want no watcher_unrecoverable for an order that reaches http", kinds(items))
	}
}

// The failsafe one-shot-resets the attempt counter when it fires. That reset
// lives in another package, so the two boot checks guard against each other
// here rather than trusting it.
func TestBootFailsafeAndBootAttemptsDoNotBothFire(t *testing.T) {
	at := now.Add(-2 * time.Hour)
	n := connected()
	n.BootFailsafeAt = &at
	n.BootAttemptCount = 2
	items := Evaluate(n, Facts{Now: now})
	if has(items, KindBootFailsafe) == nil || has(items, KindBootAttemptsFailing) != nil {
		t.Errorf("kinds = %v, want boot_failsafe only", kinds(items))
	}

	n.BootFailsafeAt = nil
	items = Evaluate(n, Facts{Now: now})
	got := has(items, KindBootAttemptsFailing)
	if got == nil {
		t.Fatalf("kinds = %v, want boot_attempts_failing", kinds(items))
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want the attempt count", got.Count)
	}
}

// IsRecoveryBootstrap reads the last system-info report. For an offline node
// that report can describe a boot from days ago, so it is not evidence of
// where the device is now.
func TestRecoveryIsOnlyReportedForAConnectedNode(t *testing.T) {
	n := connected()
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{
		Boot: &node.BootInfo{RecoveryBootstrap: true},
	}}
	if has(Evaluate(n, Facts{Now: now}), KindInRecovery) == nil {
		t.Error("a connected node running the recovery image must be reported")
	}
	n.Status = node.StatusOffline
	items := Evaluate(n, Facts{Now: now})
	if has(items, KindInRecovery) != nil {
		t.Errorf("kinds = %v, want no in_recovery for an offline node", kinds(items))
	}
}

// Every kind the API publishes must have a severity path and appear in the
// inventory the UI builds its filter from, or a new check ships invisible.
func TestEveryKindIsInventoried(t *testing.T) {
	seen := map[Kind]bool{}
	for _, k := range AllKinds() {
		if seen[k] {
			t.Errorf("%s is listed twice", k)
		}
		seen[k] = true
		if s := SeverityFor(k); s != SeverityWarning && s != SeverityCritical {
			t.Errorf("%s has severity %q", k, s)
		}
	}
	for k := range severities {
		if !seen[k] {
			t.Errorf("%s has a severity but is not in AllKinds()", k)
		}
	}
}

// A netbooted node reports the agent baked into boot.img, not the one on the
// node's disk, so "behind the latest release" would be a false verdict there —
// and the update_agent the row would invite is managed-OS-only and could never
// be delivered. The version on file answers a different question in recovery,
// so the check must not ask it.
func TestAgentUpdateIsSilentOnTheRecoveryBootstrap(t *testing.T) {
	n := connected()
	n.AgentVersion = "0.6.13"
	facts := Facts{Now: now, LatestAgentVersion: "0.7.1"}

	if has(Evaluate(n, facts), KindAgentOutdated) == nil {
		t.Fatal("an installed-OS node behind the newest release must still be reported")
	}

	// Agents from 0.4.5 say so directly...
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{
		Boot: &node.BootInfo{RecoveryBootstrap: true},
	}}
	if items := Evaluate(n, facts); has(items, KindAgentOutdated) != nil {
		t.Errorf("kinds = %v, want no agent_outdated on the recovery bootstrap", kinds(items))
	}

	// ...and older ones are caught by the boot-device fallback.
	n.SystemInfo.Hardware.Boot = &node.BootInfo{CurrentBootDevice: "http"}
	if items := Evaluate(n, facts); has(items, KindAgentOutdated) != nil {
		t.Errorf("kinds = %v, want no agent_outdated for a netbooted node", kinds(items))
	}
}

// The defect this pins invites needless EEPROM flashes, one after another.
// The flash does not reboot — the agent says so in as many words —
// so the device keeps REPORTING the old firmware, bootloader_outdated stays
// true, and the row goes on offering the button that was just pressed.
func TestAFlashAwaitingItsRebootReplacesTheOutdatedRow(t *testing.T) {
	const cm5Rev = "d04170"
	boot := now.Add(-48 * time.Hour)
	flash := now.Add(-5 * time.Minute)

	n := connected()
	n.BootloaderVersion = "aa11bb22"
	n.LastBootAt = &boot
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{
		CPU: &node.CPUInfo{BoardRevision: cm5Rev},
	}}
	facts := Facts{Now: now, BaseFirmware: map[string]string{"cm5": "f68405bc"}}

	// Nothing flashed yet: the freshness verdict stands.
	if has(Evaluate(n, facts), KindBootloaderOutdated) == nil {
		t.Fatal("a node on older firmware with no pending flash must be reported as outdated")
	}

	// Flashed after the last boot: one row, and it is the pending one.
	n.BootloaderFlashedAt = &flash
	items := Evaluate(n, facts)
	if has(items, KindBootloaderOutdated) != nil {
		t.Errorf("kinds = %v, want no bootloader_outdated while a flash awaits its reboot", kinds(items))
	}
	pending := has(items, KindBootloaderRebootPending)
	if pending == nil {
		t.Fatalf("kinds = %v, want bootloader_reboot_pending", kinds(items))
	}
	if pending.Since == nil || !pending.Since.Equal(flash) {
		t.Errorf("since = %v, want the flash time %v", pending.Since, flash)
	}

	// Rebooted since, and still behind: the flash did not take, and the
	// freshness verdict is the honest one again.
	rebooted := now.Add(-time.Minute)
	n.LastBootAt = &rebooted
	items = Evaluate(n, facts)
	if has(items, KindBootloaderRebootPending) != nil {
		t.Errorf("kinds = %v, want no pending row after the node has booted", kinds(items))
	}
	if has(items, KindBootloaderOutdated) == nil {
		t.Errorf("kinds = %v, want bootloader_outdated again", kinds(items))
	}
}

// A node that has never reported a boot time cannot be judged either way.
// Claiming the flash is still pending would hide the freshness check behind a
// state nothing ever clears.
func TestAFlashIsNotPendingWithoutAKnownBootTime(t *testing.T) {
	n := connected()
	n.LastBootAt = nil
	flashed := now.Add(-time.Minute)
	n.BootloaderFlashedAt = &flashed
	items := Evaluate(n, Facts{Now: now})
	if has(items, KindBootloaderRebootPending) != nil {
		t.Errorf("kinds = %v, want no pending row without a boot time", kinds(items))
	}
}

// Applying a profile writes config.txt and the hardware comes up on the next
// boot — the same shape as the bootloader, and it was missing for the same
// reason: an apply that succeeded left the row saying "not applied".
func TestAnAppliedProfileWaitsForItsReboot(t *testing.T) {
	profile := &device_profile.Equipage{}
	boot := now.Add(-48 * time.Hour)
	applied := now.Add(-10 * time.Minute)

	n := connected()
	n.DeviceProfile = profile
	n.LastBootAt = &boot

	// Declared and never applied: the pending-apply row, not the reboot one.
	items := Evaluate(n, Facts{Now: now})
	if has(items, KindDeviceProfilePending) == nil {
		t.Fatalf("kinds = %v, want device_profile_pending", kinds(items))
	}
	if has(items, KindDeviceProfileRebootPending) != nil {
		t.Errorf("kinds = %v, want no reboot row before anything was applied", kinds(items))
	}

	// Applied after the last boot: the reboot row replaces it.
	n.DeviceProfileAppliedHash = "abc123"
	n.DeviceProfileAppliedAt = &applied
	items = Evaluate(n, Facts{Now: now})
	if has(items, KindDeviceProfilePending) != nil {
		t.Errorf("kinds = %v, want no pending-apply row once applied", kinds(items))
	}
	pending := has(items, KindDeviceProfileRebootPending)
	if pending == nil {
		t.Fatalf("kinds = %v, want device_profile_reboot_pending", kinds(items))
	}
	if pending.Since == nil || !pending.Since.Equal(applied) {
		t.Errorf("since = %v, want the apply time %v", pending.Since, applied)
	}

	// Booted since: nothing to say.
	rebooted := now.Add(-time.Minute)
	n.LastBootAt = &rebooted
	if items := Evaluate(n, Facts{Now: now}); len(items) != 0 {
		t.Errorf("kinds = %v, want none after the node has booted", kinds(items))
	}

	// No boot time reported: tri-state, and nil means do not judge.
	n.LastBootAt = nil
	if items := Evaluate(n, Facts{Now: now}); has(items, KindDeviceProfileRebootPending) != nil {
		t.Errorf("kinds = %v, want no verdict without a boot time", kinds(items))
	}
}

// The shape a lost terminal report leaves behind, and the reason this check
// exists: an agent update replaces the agent's binary while a utility apply is
// running in the same process, the apply fails, and the failure never leaves the
// device. The abandonment sweep runs on RECONNECT and the replacement agent has
// already reconnected, so nothing sweeps it — the command stays in_progress on a
// node that is online throughout, and every action on that node stays withheld.
func TestACommandThatHasSaidNothingForTooLongIsStuck(t *testing.T) {
	n := connected()

	fresh := Facts{Now: now, Active: map[uuid.UUID]ActiveCommand{
		n.ID: {Slug: "apply_utility_config", UpdatedAt: now.Add(-2 * time.Minute)},
	}}
	if items := Evaluate(n, fresh); has(items, KindCommandStuck) != nil {
		t.Errorf("kinds = %v, want nothing for a command that reported two minutes ago", kinds(items))
	}

	silent := now.Add(-35 * time.Minute)
	items := Evaluate(n, Facts{Now: now, Active: map[uuid.UUID]ActiveCommand{
		n.ID: {Slug: "apply_utility_config", UpdatedAt: silent},
	}})
	stuck := has(items, KindCommandStuck)
	if stuck == nil {
		t.Fatalf("kinds = %v, want command_stuck", kinds(items))
	}
	if stuck.Subject != "apply_utility_config" {
		t.Errorf("subject = %q, want the command that is stuck", stuck.Subject)
	}
	if stuck.Since == nil || !stuck.Since.Equal(silent) {
		t.Errorf("since = %v, want the last thing the node said, %v", stuck.Since, silent)
	}

	// A row with no timestamp at all is not evidence of silence.
	if items := Evaluate(n, Facts{Now: now, Active: map[uuid.UUID]ActiveCommand{
		n.ID: {Slug: "apply_utility_config"},
	}}); has(items, KindCommandStuck) != nil {
		t.Errorf("kinds = %v, want no verdict without a report time", kinds(items))
	}
}

// The access self-heal stands down after three consecutive failures, and until
// now that stand-down was an ERROR log line and nothing else — the code that
// writes it says a silent stand-down "would read as converged". This reports
// the FIRST failure instead: strictly earlier, and free.
func TestAFailedAccessApplyIsReported(t *testing.T) {
	n := connected()

	if items := Evaluate(n, Facts{Now: now}); has(items, KindAccessApplyFailed) != nil {
		t.Errorf("kinds = %v, want nothing without a failed apply", kinds(items))
	}

	// A node OTHER than this one failing must not implicate it.
	other := Facts{Now: now, AccessApplyFailed: map[uuid.UUID]bool{uuid.New(): true}}
	if items := Evaluate(n, other); has(items, KindAccessApplyFailed) != nil {
		t.Errorf("kinds = %v, want nothing for another node's failure", kinds(items))
	}

	mine := Facts{Now: now, AccessApplyFailed: map[uuid.UUID]bool{n.ID: true}}
	if has(Evaluate(n, mine), KindAccessApplyFailed) == nil {
		t.Error("a node whose last access apply failed must be reported")
	}
}

// At the cap, a node waiting to be admitted — created and not yet connected,
// or pending approval — will be refused when it asks, and the row says so
// with the two numbers. An admitted node and a rejected one are not waiting;
// below the cap, under an unlimited one, or with the count unknown, nothing
// is reported.
func TestDeviceCapReachedMarksNodesWaitingForAdmission(t *testing.T) {
	atCap := &DeviceCap{Cap: 25, Admitted: 25, AccountDevices: 3, InstanceOperator: true}
	waiting := connected()
	waiting.Status = node.StatusReadyForAdoption
	waiting.ApprovalStatus = node.ApprovalStatusApproved
	pending := connected()
	pending.Status = node.StatusOffline
	pending.ApprovalStatus = node.ApprovalStatusPendingApproval
	admitted := connected()
	admitted.ApprovalStatus = node.ApprovalStatusApproved
	rejected := connected()
	rejected.ApprovalStatus = node.ApprovalStatusRejected

	for _, n := range []*node.Node{waiting, pending} {
		it := has(Evaluate(n, Facts{Now: now, DeviceCap: atCap}), KindDeviceCapReached)
		if it == nil {
			t.Fatalf("a node waiting for admission at the cap was not reported (status %s, %s)", n.Status, n.ApprovalStatus)
		}
		if it.Current != "25" || it.Expected != "25" || it.Severity != SeverityWarning ||
			it.AccountDevices == nil || *it.AccountDevices != 3 || it.InstanceOperator == nil || !*it.InstanceOperator {
			t.Errorf("item = %+v, want the admitted count and the cap as raw values, a warning", *it)
		}
	}
	for _, n := range []*node.Node{admitted, rejected} {
		if has(Evaluate(n, Facts{Now: now, DeviceCap: atCap}), KindDeviceCapReached) != nil {
			t.Errorf("a node that is not waiting (%s) was reported", n.ApprovalStatus)
		}
	}
	for _, c := range []*DeviceCap{nil, {Cap: 25, Admitted: 24}, {Cap: 0, Admitted: 1000}} {
		if has(Evaluate(waiting, Facts{Now: now, DeviceCap: c}), KindDeviceCapReached) != nil {
			t.Errorf("reported with cap facts %+v", c)
		}
	}
}

// A caller who does not operate the install
// is told the cap and its OWN admitted count, never the install-wide count.
func TestDeviceCapReachedShowsATenantOnlyItsOwnCount(t *testing.T) {
	waiting := connected()
	waiting.Status = node.StatusReadyForAdoption
	waiting.ApprovalStatus = node.ApprovalStatusApproved
	it := has(Evaluate(waiting, Facts{Now: now, DeviceCap: &DeviceCap{Cap: 25, Admitted: 97, AccountDevices: 4}}),
		KindDeviceCapReached)
	if it == nil {
		t.Fatal("not reported")
	}
	if it.Current != "" {
		t.Errorf("the install-wide count reached a tenant: Current = %q", it.Current)
	}
	if it.Expected != "25" || it.AccountDevices == nil || *it.AccountDevices != 4 ||
		it.InstanceOperator == nil || *it.InstanceOperator {
		t.Errorf("tenant item = %+v", *it)
	}
}

// At the account's own limit, a node waiting to be admitted is reported with
// the account's limit and count; an admitted or rejected node is not, nor a
// node of an account below its limit, nor while the instance cap is reached
// (that is the refusal admission meets first, reported as its own kind).
func TestAccountDeviceLimitReachedMarksNodesWaitingForAdmission(t *testing.T) {
	full := &AccountDeviceLimit{Limit: 5, AccountDevices: 7}
	waiting := connected()
	waiting.Status = node.StatusReadyForAdoption
	waiting.ApprovalStatus = node.ApprovalStatusApproved
	admitted := connected()
	admitted.ApprovalStatus = node.ApprovalStatusApproved

	it := has(Evaluate(waiting, Facts{Now: now, AccountDeviceLimit: full}), KindAccountDeviceLimitReached)
	if it == nil {
		t.Fatal("a node waiting at the account's limit was not reported")
	}
	if it.Expected != "5" || it.AccountDevices == nil || *it.AccountDevices != 7 || it.Current != "" {
		t.Errorf("item = %+v, want the account's limit and its own count", *it)
	}
	if has(Evaluate(admitted, Facts{Now: now, AccountDeviceLimit: full}), KindAccountDeviceLimitReached) != nil {
		t.Error("an admitted node of an account over its limit was reported")
	}
	for _, l := range []*AccountDeviceLimit{nil, {Limit: 5, AccountDevices: 4}, {Limit: 0, AccountDevices: 99}} {
		if has(Evaluate(waiting, Facts{Now: now, AccountDeviceLimit: l}), KindAccountDeviceLimitReached) != nil {
			t.Errorf("reported with account-limit facts %+v", l)
		}
	}
	both := Evaluate(waiting, Facts{Now: now, AccountDeviceLimit: full, DeviceCap: &DeviceCap{Cap: 25, Admitted: 25}})
	if has(both, KindAccountDeviceLimitReached) != nil || has(both, KindDeviceCapReached) == nil {
		t.Error("with both reached, the row must name the instance cap, which admission checks first")
	}
}
