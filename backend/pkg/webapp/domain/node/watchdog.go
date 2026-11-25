package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// WatchdogConfig is a node's desired watchdog supervision. It is stored per
// node and converged by the agent's apply_watchdog_config command.
//
// The whole feature can reboot a physical device in a loop, so every bound here
// is deliberate rather than cosmetic: the numbers are what the hardware and the
// daemon can actually honour, and the cross-field rules are the combinations
// that reboot a healthy node.
type WatchdogConfig struct {
	Enabled bool `json:"enabled"`

	// OS hang: the daemon pets /dev/watchdog; the kernel reboots if it stops.
	OSHang             bool `json:"os_hang"`
	WatchdogTimeoutSec int  `json:"watchdog_timeout_sec"`

	// Network: reboot when the node cannot reach any of these.
	Network     bool     `json:"network"`
	PingTargets []string `json:"ping_targets,omitempty"`
	PingCount   int      `json:"ping_count,omitempty"`
	Interface   string   `json:"interface,omitempty"`

	// Agent health: reboot when the agent stops refreshing its liveness file.
	AgentHealth    bool `json:"agent_health"`
	AgentChangeSec int  `json:"agent_change_sec,omitempty"`

	// Thresholds. 0 means "do not supervise this".
	MaxLoad1        int `json:"max_load_1,omitempty"`
	MaxLoad5        int `json:"max_load_5,omitempty"`
	MaxLoad15       int `json:"max_load_15,omitempty"`
	MinMemoryPages  int `json:"min_memory_pages,omitempty"`
	MaxTemperatureC int `json:"max_temperature_c,omitempty"`

	IntervalSec     int `json:"interval_sec,omitempty"`
	RetryTimeoutSec int `json:"retry_timeout_sec,omitempty"`
}

// WatchdogDisarmedStatus is the StatusUpdate.status token the agent sends when
// its on-device breaker takes the watchdog down (three restarts of the daemon
// without a stable run). It arrives on the generic status channel because the
// breaker trips with no command in flight, but it is a watchdog fact, not a
// node connection state — it must never be written into nodes.status.
const WatchdogDisarmedStatus = "watchdog_disarmed"

// PlatformDisarmReason is the reason the agent records when the PLATFORM asked
// for the watchdog to be switched off, as opposed to its local breaker tripping
// after repeated reboots. It is a wire contract with the agent: keep it in step
// with disableWatchdogSupervision.
const PlatformDisarmReason = "disabled by the platform"

// IsPlatformDisarmReason reports whether a disarm the agent reported was the
// platform's own doing.
//
// It exists because one agent flag carried both events until 0.4.1: a routine
// DELETE /watchdog left the device in the same "disarmed" state as a runaway
// reboot loop, and the agent re-reported it on every start. The platform then
// alarmed on its own command — a red "the device disarmed itself after repeated
// reboots" banner and fleet pill for something the operator had just asked for,
// which is exactly the way to teach people to ignore that pill. Matching the
// string covers agents already in the field; newer ones also carry an explicit
// source.
func IsPlatformDisarmReason(reason string) bool {
	return strings.TrimSpace(reason) == PlatformDisarmReason
}

// Watchdog presets. They exist because the useful configurations are few and
// the dangerous ones are easy to type by hand.
const (
	// WatchdogPresetAgentHealth — the watchdog supervises the agent: if the
	// agent dies or wedges, the node reboots. The safest default, because it
	// depends on nothing outside the device.
	WatchdogPresetAgentHealth = "agent-health"
	// WatchdogPresetOSHang — only the classic hardware watchdog: reboot when
	// the kernel stops responding.
	WatchdogPresetOSHang = "os-hang"
	// WatchdogPresetNetwork — reboot when the node loses the network. Needs a
	// ping target and fate-shares with whatever is pinged.
	WatchdogPresetNetwork = "network"
	// WatchdogPresetFull — OS hang + agent health together.
	WatchdogPresetFull = "full"
)

// WatchdogPreset returns a ready configuration for a named preset. gateway is
// used only by the network preset and may be empty otherwise.
func WatchdogPreset(name, gateway string) (WatchdogConfig, error) {
	base := WatchdogConfig{
		Enabled:            true,
		WatchdogTimeoutSec: 15,
		IntervalSec:        10,
		RetryTimeoutSec:    60,
	}
	switch name {
	case WatchdogPresetAgentHealth:
		base.AgentHealth = true
		base.AgentChangeSec = 300
	case WatchdogPresetOSHang:
		base.OSHang = true
	case WatchdogPresetNetwork:
		if gateway == "" {
			return WatchdogConfig{}, fmt.Errorf("the network preset needs a ping target")
		}
		base.Network = true
		base.PingTargets = []string{gateway}
		base.PingCount = 3
	case WatchdogPresetFull:
		base.OSHang = true
		base.AgentHealth = true
		base.AgentChangeSec = 300
	default:
		return WatchdogConfig{}, fmt.Errorf("unknown watchdog preset %q", name)
	}
	return base, nil
}

// WatchdogPresetInfo is one catalog entry: the preset's identity, the operator
// wording that belongs with it, and the configuration it expands to. It exists
// so the four presets — and above all the sentence describing what the device
// will do once armed — are defined once, on the side that owns the bounds,
// instead of being retyped in every client.
type WatchdogPresetInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Consequence is what a confirmation prompt must say before arming. The
	// network preset's text carries the literal placeholder WatchdogTargetToken,
	// because the sentence is only true of a specific address.
	Consequence string `json:"consequence"`
	// RequiresPingTarget marks the preset that cannot be expanded without an
	// address; its Config therefore has no ping targets.
	RequiresPingTarget bool           `json:"requires_ping_target"`
	Config             WatchdogConfig `json:"config"`
}

// WatchdogTargetToken is the placeholder a client substitutes with the ping
// target it collected before arming the network preset.
const WatchdogTargetToken = "{{target}}"

// WatchdogPresetCatalog returns the four presets with their operator wording.
// The configurations come from WatchdogPreset itself, so the catalog can never
// describe an expansion the arming path would not produce.
func WatchdogPresetCatalog() []WatchdogPresetInfo {
	// The network preset needs an address to expand at all; the catalog answers
	// before one is known, so it expands with a placeholder and hands back an
	// entry with no targets. RequiresPingTarget is what the client acts on.
	network, _ := WatchdogPreset(WatchdogPresetNetwork, "192.0.2.1")
	network.PingTargets = nil

	agentHealth, _ := WatchdogPreset(WatchdogPresetAgentHealth, "")
	osHang, _ := WatchdogPreset(WatchdogPresetOSHang, "")
	full, _ := WatchdogPreset(WatchdogPresetFull, "")

	return []WatchdogPresetInfo{
		{
			Name:  WatchdogPresetAgentHealth,
			Label: "Watch the agent",
			Description: "Reboot if the Orchestrator agent dies or stops responding. " +
				"Depends on nothing outside the device, so it is the safest place to start.",
			Consequence: "The device will reboot itself whenever the Orchestrator agent dies or stops responding.",
			Config:      agentHealth,
		},
		{
			Name:  WatchdogPresetOSHang,
			Label: "Watch the OS",
			Description: "Hardware watchdog: reboot if the kernel stops responding. " +
				"Takes effect after the next reboot.",
			Consequence: "The device will reboot itself whenever the kernel stops responding. " +
				"Takes effect after the next reboot.",
			Config: osHang,
		},
		{
			Name:  WatchdogPresetFull,
			Label: "Watch the OS and agent",
			Description: "Reboot if the kernel or the Orchestrator agent stops responding. " +
				"The OS check takes effect after the next reboot.",
			Consequence: "The device will reboot itself whenever the kernel or the Orchestrator agent stops responding.",
			Config:      full,
		},
		{
			Name:  WatchdogPresetNetwork,
			Label: "Watch the network",
			Description: "Reboot whenever the node cannot reach the address you name, for any reason, " +
				"including a problem at the target itself. Use with care.",
			Consequence: "The device will reboot itself whenever it cannot reach " + WatchdogTargetToken +
				", for any reason, including a failure at " + WatchdogTargetToken +
				" itself or anywhere on the path to it.",
			RequiresPingTarget: true,
			Config:             network,
		},
	}
}

var interfaceRe = regexp.MustCompile(`^[a-z][a-z0-9.]{0,14}$`)

// Validate checks a desired configuration. A disabled config needs no checks —
// disarming is always allowed.
func (w *WatchdogConfig) Validate() error {
	if !w.Enabled {
		return nil
	}
	if !w.OSHang && !w.Network && !w.AgentHealth &&
		w.MaxLoad1 == 0 && w.MaxLoad5 == 0 && w.MaxLoad15 == 0 &&
		w.MinMemoryPages == 0 && w.MaxTemperatureC == 0 {
		return fmt.Errorf("enable at least one kind of supervision, or disable the watchdog")
	}

	if w.OSHang {
		// The bcm2835 hardware watchdog tops out around 15 seconds; a larger
		// value is silently clamped by the kernel, which makes the UI lie.
		if w.WatchdogTimeoutSec < 5 || w.WatchdogTimeoutSec > 15 {
			return fmt.Errorf("the hardware watchdog timeout must be between 5 and 15 seconds")
		}
	}

	if w.Network {
		if len(w.PingTargets) == 0 || len(w.PingTargets) > 4 {
			return fmt.Errorf("network supervision needs between 1 and 4 ping targets")
		}
		for _, target := range w.PingTargets {
			ip := net.ParseIP(target)
			if ip == nil {
				return fmt.Errorf("%q is not an IP address", target)
			}
			if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
				return fmt.Errorf("%q cannot be used as a ping target: loopback, multicast and all-zeros addresses are not allowed", target)
			}
		}
		if w.PingCount < 1 || w.PingCount > 10 {
			return fmt.Errorf("ping count must be between 1 and 10")
		}
	}
	if w.Interface != "" && !interfaceRe.MatchString(w.Interface) {
		return fmt.Errorf("%q is not a valid interface name", w.Interface)
	}

	if w.AgentHealth {
		// The floor has to clear an agent restart plus its reconnect backoff, or
		// a routine agent update reboots the node.
		if w.AgentChangeSec < 120 || w.AgentChangeSec > 3600 {
			return fmt.Errorf("the agent health window must be between 120 and 3600 seconds")
		}
	}

	if w.IntervalSec < 1 || w.IntervalSec > 60 {
		return fmt.Errorf("the check interval must be between 1 and 60 seconds")
	}
	if w.OSHang && w.IntervalSec >= w.WatchdogTimeoutSec {
		// A daemon that pets slower than the hardware timeout reboots a node
		// that is perfectly healthy.
		return fmt.Errorf("the check interval (%ds) must be shorter than the hardware timeout (%ds)",
			w.IntervalSec, w.WatchdogTimeoutSec)
	}
	if w.RetryTimeoutSec < 30 || w.RetryTimeoutSec > 600 {
		return fmt.Errorf("the retry timeout must be between 30 and 600 seconds")
	}

	for name, load := range map[string]int{"1-minute": w.MaxLoad1, "5-minute": w.MaxLoad5, "15-minute": w.MaxLoad15} {
		if load != 0 && load < 2 {
			// Load 1 on a four-core Pi is an ordinary afternoon.
			return fmt.Errorf("the %s load limit must be at least 2", name)
		}
	}
	if w.MaxTemperatureC != 0 && (w.MaxTemperatureC < 60 || w.MaxTemperatureC > 110) {
		return fmt.Errorf("the temperature limit must be between 60 and 110 °C")
	}
	if w.MinMemoryPages < 0 || w.MinMemoryPages > 131072 {
		// 131072 pages is 512 MiB, already half of the smallest fleet member.
		return fmt.Errorf("the minimum free memory must be between 0 and 131072 pages (512 MiB)")
	}
	return nil
}

// PublicTargets returns the ping targets that are not private addresses. The
// platform does not refuse them — some fleets legitimately watch an upstream —
// but a public target fate-shares with the WAN, so one ISP blip reboots every
// node at once, and the UI has to say so.
func (w *WatchdogConfig) PublicTargets() []string {
	var out []string
	for _, target := range w.PingTargets {
		ip := net.ParseIP(target)
		if ip == nil {
			continue
		}
		if !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			out = append(out, target)
		}
	}
	return out
}

// Hash is the desired-state fingerprint. The agent compares it to what it last
// applied and no-ops when they match, so a re-dispatch is free.
func (w *WatchdogConfig) Hash() string {
	data, err := json.Marshal(w)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
