package node

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The dispatch payload builder needs an apply_watchdog_config arm, and a
// missing one is invisible until an agent meets the version floor: the floor
// refuses every dispatch below it before the switch is reached, after which
// PUT /watchdog fails with "unknown command". These tests
// pin the arm to the shape SetWatchdog actually produces, so the case cannot go
// missing again without a red test.

// buildWatchdogPayloadFor runs the builder over the params SetWatchdog builds
// for cfg. Keeping the two in one helper is the point of the test: a field
// added to one side and not the other shows up as a missing payload key.
func buildWatchdogPayloadFor(cfg node.WatchdogConfig) map[string]interface{} {
	params := map[string]interface{}{
		"config_hash":          cfg.Hash(),
		"enabled":              cfg.Enabled,
		"os_hang":              cfg.OSHang,
		"watchdog_timeout_sec": float64(cfg.WatchdogTimeoutSec),
		"network":              cfg.Network,
		"ping_targets":         toInterfaceSlice(cfg.PingTargets),
		"ping_count":           float64(cfg.PingCount),
		"interface":            cfg.Interface,
		"agent_health":         cfg.AgentHealth,
		"agent_change_sec":     float64(cfg.AgentChangeSec),
		"max_load_1":           float64(cfg.MaxLoad1),
		"max_load_5":           float64(cfg.MaxLoad5),
		"max_load_15":          float64(cfg.MaxLoad15),
		"min_memory_pages":     float64(cfg.MinMemoryPages),
		"max_temperature_c":    float64(cfg.MaxTemperatureC),
		"interval_sec":         float64(cfg.IntervalSec),
		"retry_timeout_sec":    float64(cfg.RetryTimeoutSec),
		"verify_before_arm":    true,
	}
	payload := map[string]interface{}{}
	buildApplyWatchdogPayload(params, payload)
	return payload
}

func TestBuildApplyWatchdogPayload_CarriesEveryPresetField(t *testing.T) {
	for _, preset := range node.WatchdogPresetCatalog() {
		cfg := preset.Config
		if preset.RequiresPingTarget {
			expanded, err := node.WatchdogPreset(preset.Name, "192.168.1.1")
			if err != nil {
				t.Fatalf("%s: expanding the preset failed: %v", preset.Name, err)
			}
			cfg = expanded
		}

		payload := buildWatchdogPayloadFor(cfg)

		if payload["config_hash"] != cfg.Hash() {
			t.Errorf("%s: config_hash missing or wrong: %v", preset.Name, payload["config_hash"])
		}
		if payload["enabled"] != true {
			t.Errorf("%s: enabled should survive as true, got %v", preset.Name, payload["enabled"])
		}
		if payload["os_hang"] != cfg.OSHang || payload["agent_health"] != cfg.AgentHealth ||
			payload["network"] != cfg.Network {
			t.Errorf("%s: supervision flags did not survive: %#v", preset.Name, payload)
		}
		if payload["interval_sec"] != float64(cfg.IntervalSec) ||
			payload["retry_timeout_sec"] != float64(cfg.RetryTimeoutSec) ||
			payload["watchdog_timeout_sec"] != float64(cfg.WatchdogTimeoutSec) {
			t.Errorf("%s: timing fields did not survive: %#v", preset.Name, payload)
		}
		if cfg.AgentHealth && payload["agent_change_sec"] != float64(cfg.AgentChangeSec) {
			t.Errorf("%s: agent_change_sec did not survive: %v", preset.Name, payload["agent_change_sec"])
		}
		if cfg.Network {
			targets, ok := payload["ping_targets"].([]interface{})
			if !ok || len(targets) != len(cfg.PingTargets) {
				t.Errorf("%s: ping_targets did not survive: %#v", preset.Name, payload["ping_targets"])
			}
			if payload["ping_count"] != float64(cfg.PingCount) {
				t.Errorf("%s: ping_count did not survive: %v", preset.Name, payload["ping_count"])
			}
		}
	}
}

func TestBuildApplyWatchdogPayload_ForcesVerifyBeforeArm(t *testing.T) {
	// The on-device pre-arm checks are what turn a typo'd ping target into an
	// error instead of a device that reboots every minute, so a caller must not
	// be able to switch them off.
	payload := map[string]interface{}{}
	buildApplyWatchdogPayload(map[string]interface{}{
		"enabled":           true,
		"verify_before_arm": false,
	}, payload)

	if payload["verify_before_arm"] != true {
		t.Fatalf("verify_before_arm must be forced true, got %v", payload["verify_before_arm"])
	}
}

func TestBuildApplyWatchdogPayload_DropsWrongTypesAndUnknownKeys(t *testing.T) {
	// Numbers are matched as float64 only — the JSON shape SetWatchdog casts to.
	// An int would be dropped silently, which is worth pinning: it is the exact
	// mistake a future caller building params by hand would make.
	payload := map[string]interface{}{}
	buildApplyWatchdogPayload(map[string]interface{}{
		"enabled":          true,
		"interval_sec":     10, // int, not float64
		"interface":        "  eth0  ",
		"smuggled":         "should not appear",
		"min_memory_pages": float64(4096),
	}, payload)

	if _, present := payload["interval_sec"]; present {
		t.Errorf("an int-typed number should not reach the payload, got %v", payload["interval_sec"])
	}
	if _, present := payload["smuggled"]; present {
		t.Error("the builder must copy only the keys it knows")
	}
	if payload["interface"] != "eth0" {
		t.Errorf("interface should be trimmed, got %q", payload["interface"])
	}
	if payload["min_memory_pages"] != float64(4096) {
		t.Errorf("min_memory_pages did not survive: %v", payload["min_memory_pages"])
	}
}

func TestBuildApplyWatchdogPayload_DisarmCarriesEnabledFalse(t *testing.T) {
	// A disarm is the same command with enabled=false. If the flag were dropped
	// for being a zero value, a disarm would read as an arm on the device.
	payload := buildWatchdogPayloadFor(node.WatchdogConfig{Enabled: false})

	if payload["enabled"] != false {
		t.Fatalf("a disarm must carry enabled=false, got %v", payload["enabled"])
	}
}
