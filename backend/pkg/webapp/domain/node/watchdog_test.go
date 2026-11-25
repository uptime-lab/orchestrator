package node

import (
	"strings"
	"testing"
)

func validConfig() WatchdogConfig {
	return WatchdogConfig{
		Enabled:            true,
		OSHang:             true,
		WatchdogTimeoutSec: 15,
		IntervalSec:        10,
		RetryTimeoutSec:    60,
	}
}

func TestWatchdogValidateAcceptsASaneConfig(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("expected the baseline config to validate, got %v", err)
	}
}

func TestWatchdogDisabledNeedsNoChecks(t *testing.T) {
	c := WatchdogConfig{Enabled: false, WatchdogTimeoutSec: 9999}
	if err := c.Validate(); err != nil {
		t.Fatalf("disarming must always be allowed, got %v", err)
	}
}

// The old panel accepted timeouts up to 120s while the bcm2835 hardware caps at
// ~15s, so the kernel silently clamped it and the UI lied about what was armed.
func TestWatchdogTimeoutIsBoundedByHardware(t *testing.T) {
	c := validConfig()
	c.WatchdogTimeoutSec = 120
	if err := c.Validate(); err == nil {
		t.Fatal("expected a timeout beyond the hardware cap to be refused")
	}
}

// A daemon that pets the hardware watchdog slower than its timeout reboots a
// node that is perfectly healthy.
func TestWatchdogIntervalMustBeShorterThanTimeout(t *testing.T) {
	c := validConfig()
	c.IntervalSec = 15
	if err := c.Validate(); err == nil {
		t.Fatal("expected interval >= timeout to be refused")
	}
}

func TestWatchdogNeedsAtLeastOneSupervisor(t *testing.T) {
	c := WatchdogConfig{Enabled: true, IntervalSec: 10, RetryTimeoutSec: 60}
	if err := c.Validate(); err == nil {
		t.Fatal("expected an enabled config with nothing supervised to be refused")
	}
}

func TestWatchdogPingTargetsAreValidated(t *testing.T) {
	cases := map[string]string{
		"not an ip":   "gateway.local",
		"loopback":    "127.0.0.1",
		"unspecified": "0.0.0.0",
		"multicast":   "224.0.0.1",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			c.Network = true
			c.PingCount = 3
			c.PingTargets = []string{target}
			if err := c.Validate(); err == nil {
				t.Fatalf("expected %q to be refused", target)
			}
		})
	}

	c := validConfig()
	c.Network = true
	c.PingCount = 3
	c.PingTargets = []string{"192.168.1.1"}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected a LAN gateway to be accepted, got %v", err)
	}
}

// A public ping target is allowed but reported: it fate-shares with the WAN, so
// one ISP blip reboots the whole fleet at once.
func TestWatchdogPublicTargetsAreReported(t *testing.T) {
	c := validConfig()
	c.Network = true
	c.PingCount = 3
	c.PingTargets = []string{"192.168.1.1", "8.8.8.8"}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected the config to validate, got %v", err)
	}
	public := c.PublicTargets()
	if len(public) != 1 || public[0] != "8.8.8.8" {
		t.Fatalf("expected 8.8.8.8 to be reported as public, got %v", public)
	}
}

// The agent-health window has to clear an agent restart plus its reconnect
// backoff, or a routine agent update reboots the node.
func TestWatchdogAgentWindowHasAFloor(t *testing.T) {
	c := validConfig()
	c.AgentHealth = true
	c.AgentChangeSec = 60
	if err := c.Validate(); err == nil {
		t.Fatal("expected an agent window below the floor to be refused")
	}
}

func TestWatchdogPresets(t *testing.T) {
	for _, name := range []string{WatchdogPresetAgentHealth, WatchdogPresetOSHang, WatchdogPresetFull} {
		preset, err := WatchdogPreset(name, "")
		if err != nil {
			t.Fatalf("preset %s: %v", name, err)
		}
		if verr := preset.Validate(); verr != nil {
			t.Fatalf("preset %s does not validate: %v", name, verr)
		}
	}

	if _, err := WatchdogPreset(WatchdogPresetNetwork, ""); err == nil {
		t.Fatal("the network preset must refuse to build without a target")
	}
	netPreset, err := WatchdogPreset(WatchdogPresetNetwork, "192.168.1.1")
	if err != nil || netPreset.Validate() != nil {
		t.Fatalf("network preset with a gateway should validate, got %v", err)
	}

	if _, err := WatchdogPreset("nonsense", ""); err == nil {
		t.Fatal("an unknown preset must be refused")
	}
}

func TestWatchdogHashChangesWithTheConfig(t *testing.T) {
	a := validConfig()
	b := validConfig()
	if a.Hash() != b.Hash() {
		t.Fatal("identical configs must hash identically — the agent no-ops on this")
	}
	b.WatchdogTimeoutSec = 10
	if a.Hash() == b.Hash() {
		t.Fatal("a changed config must change the hash, or the agent will skip applying it")
	}
}

func TestWatchdogPresetCatalog_MatchesTheArmingPath(t *testing.T) {
	// The catalog is what a client arms from, so an entry describing a
	// configuration the arming path would not produce is a lie the operator
	// acts on. Every entry must expand to exactly what WatchdogPreset gives.
	catalog := WatchdogPresetCatalog()
	if len(catalog) != 4 {
		t.Fatalf("expected the four presets, got %d", len(catalog))
	}

	seen := map[string]bool{}
	for _, entry := range catalog {
		if seen[entry.Name] {
			t.Errorf("%s appears twice in the catalog", entry.Name)
		}
		seen[entry.Name] = true

		if entry.Label == "" || entry.Description == "" || entry.Consequence == "" {
			t.Errorf("%s: a preset without its operator wording cannot be confirmed safely", entry.Name)
		}
		if err := entry.Config.Validate(); err != nil && !entry.RequiresPingTarget {
			t.Errorf("%s: the catalog offers a config the arming path refuses: %v", entry.Name, err)
		}

		target := ""
		if entry.RequiresPingTarget {
			target = "192.168.1.1"
		}
		expanded, err := WatchdogPreset(entry.Name, target)
		if err != nil {
			t.Fatalf("%s: expansion failed: %v", entry.Name, err)
		}
		if entry.RequiresPingTarget {
			// The catalog answers before an address is known, so it carries none.
			if len(entry.Config.PingTargets) != 0 {
				t.Errorf("%s: a preset needing a target must not ship one", entry.Name)
			}
			expanded.PingTargets = nil
		}
		if entry.Config.Hash() != expanded.Hash() {
			t.Errorf("%s: the catalog config differs from what arming would apply", entry.Name)
		}
	}
}

func TestWatchdogPresetCatalog_NetworkConsequenceNamesTheTarget(t *testing.T) {
	// The network preset's consequence is only true of a specific address, so
	// the sentence must carry the placeholder a client substitutes — otherwise
	// the confirmation understates what the operator is about to arm.
	for _, entry := range WatchdogPresetCatalog() {
		hasToken := strings.Contains(entry.Consequence, WatchdogTargetToken)
		if entry.RequiresPingTarget && !hasToken {
			t.Errorf("%s: the consequence must name the target", entry.Name)
		}
		if !entry.RequiresPingTarget && hasToken {
			t.Errorf("%s: a preset with no target must not leave a placeholder in its text", entry.Name)
		}
	}
}
