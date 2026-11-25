package node

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Live watchdog status. Everything else the platform knows about a node's
// watchdog is a memory of the last converge command: the applied hash it
// reported, the refusal it returned, the breaker report it pushed. None of that
// answers "is the daemon running right now" — a hand-run `systemctl stop
// watchdog.service` leaves the platform saying "in sync" forever.
//
// So the probe reads the device instead of the database, through the same fixed
// read-only run_script shape the OS-user and EEPROM-config scans use: a
// constant script (never operator-supplied), parsed server-side, with the raw
// output never returned. It needs no agent release — run_script has no floor.

// WatchdogStatusScanScript is the fixed script a live watchdog probe dispatches.
// Every line is a read; the KEY=VALUE shape keeps the parser trivial and makes a
// missing field indistinguishable from an empty one, which is what we want (an
// absent breaker file and an unreadable one are both "unknown").
const WatchdogStatusScanScript = `echo "service_state=$(systemctl is-active watchdog.service 2>/dev/null)"
echo "service_enabled=$(systemctl is-enabled watchdog.service 2>/dev/null)"
echo "device=$([ -e /dev/watchdog ] && echo present || echo absent)"
echo "conf_present=$([ -f /etc/watchdog.conf ] && echo yes || echo no)"
echo "drop_in_present=$([ -f /etc/systemd/system/watchdog.service.d/10-orchestrator.conf ] && echo yes || echo no)"
echo "breaker=$(tr -d '\n' < /var/lib/orchestrator-agent/watchdog-armed 2>/dev/null)"
echo "liveness_age_sec=$(if [ -e /run/orchestrator-agent/alive ]; then expr $(date +%s) - $(stat -c %Y /run/orchestrator-agent/alive); fi)"`

// WatchdogStatusScanResult is the polled outcome of a live watchdog probe.
type WatchdogStatusScanResult struct {
	Status string `json:"status"` // "running" | "completed" | "failed"
	Detail string `json:"detail,omitempty"`

	// ServiceState / ServiceEnabled are systemd's own words ("active",
	// "inactive", "enabled", "disabled", ...). Empty means the probe could not
	// tell — never assume "inactive" from an empty answer.
	ServiceState   string `json:"service_state,omitempty"`
	ServiceEnabled string `json:"service_enabled,omitempty"`

	// DevicePresent is /dev/watchdog. Absent means the hardware watchdog has not
	// attached yet (it needs the config.txt dtparam plus a reboot), which the OS
	// hang preset depends on.
	DevicePresent bool `json:"device_present"`
	ConfPresent   bool `json:"conf_present"`
	DropInPresent bool `json:"drop_in_present"`

	// Breaker state, as the agent persisted it. AppliedHash here is what the
	// DEVICE believes it applied, so a mismatch against the desired hash is a
	// drift the command history cannot show.
	BreakerKnown    bool   `json:"breaker_known"`
	AppliedHash     string `json:"applied_hash,omitempty"`
	BreakerBootID   string `json:"breaker_boot_id,omitempty"`
	BreakerBoots    int    `json:"breaker_boots,omitempty"`
	BreakerDisarmed bool   `json:"breaker_disarmed"`
	// DisarmedByPlatform is the benign counterpart: supervision is off because
	// the platform asked, not because anything went wrong.
	DisarmedByPlatform bool   `json:"disarmed_by_platform"`
	BreakerReason      string `json:"breaker_reason,omitempty"`

	// LivenessAgeSec is how long ago the agent last refreshed the file the
	// agent-health preset supervises. Nil when the file is absent — which on an
	// armed agent-health node is itself the finding.
	LivenessAgeSec *int `json:"liveness_age_sec,omitempty"`
}

// deviceWatchdogState mirrors the agent's on-disk breaker state. It is a copy
// rather than a shared type because the two live in different modules; the
// field names are the wire contract and must not drift.
type deviceWatchdogState struct {
	ConfigHash   string `json:"config_hash"`
	BootID       string `json:"boot_id"`
	BootCount    int    `json:"boot_count"`
	Disarmed     bool   `json:"disarmed"`
	DisarmReason string `json:"disarm_reason"`
	// DisarmSource distinguishes the local breaker from a platform-requested
	// disable (agent 0.4.1+). Empty predates the field and reads as the breaker,
	// which is how every already-fielded agent behaves.
	DisarmSource string `json:"disarm_source"`
}

// ParseWatchdogStatus turns the probe's KEY=VALUE output into the result. It
// never fails: an unparseable line is a field we do not know, and a probe that
// answered partially still tells the operator more than the database does.
func ParseWatchdogStatus(stdout string) WatchdogStatusScanResult {
	res := WatchdogStatusScanResult{Status: "completed"}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "service_state":
			res.ServiceState = value
		case "service_enabled":
			res.ServiceEnabled = value
		case "device":
			res.DevicePresent = value == "present"
		case "conf_present":
			res.ConfPresent = value == "yes"
		case "drop_in_present":
			res.DropInPresent = value == "yes"
		case "breaker":
			if value == "" {
				continue
			}
			var st deviceWatchdogState
			if err := json.Unmarshal([]byte(value), &st); err != nil {
				continue
			}
			res.BreakerKnown = true
			res.AppliedHash = st.ConfigHash
			res.BreakerBootID = st.BootID
			res.BreakerBoots = st.BootCount
			// A disarm the platform ASKED for is not a breaker trip. Reporting
			// it as one made the probe raise a fault for the most ordinary
			// action there is — switching the watchdog off — and spent the one
			// signal this probe exists to surface.
			res.BreakerDisarmed = st.Disarmed &&
				st.DisarmSource != "platform" && !IsPlatformDisarmReason(st.DisarmReason)
			res.BreakerReason = st.DisarmReason
			res.DisarmedByPlatform = st.Disarmed && !res.BreakerDisarmed
		case "liveness_age_sec":
			if value == "" {
				continue
			}
			age, err := strconv.Atoi(value)
			if err != nil || age < 0 {
				continue
			}
			res.LivenessAgeSec = &age
		}
	}
	return res
}
