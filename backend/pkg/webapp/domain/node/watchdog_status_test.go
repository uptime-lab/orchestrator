package node

import (
	"strings"
	"testing"
)

func TestParseWatchdogStatus_FullProbe(t *testing.T) {
	out := strings.Join([]string{
		"service_state=active",
		"service_enabled=enabled",
		"device=present",
		"conf_present=yes",
		"drop_in_present=yes",
		`breaker={"config_hash":"abc123","boot_id":"b-1","boot_count":1,"disarmed":false}`,
		"liveness_age_sec=7",
	}, "\n")

	res := ParseWatchdogStatus(out)

	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if res.ServiceState != "active" || res.ServiceEnabled != "enabled" {
		t.Errorf("service state/enabled = %q/%q", res.ServiceState, res.ServiceEnabled)
	}
	if !res.DevicePresent || !res.ConfPresent || !res.DropInPresent {
		t.Errorf("presence flags = %v/%v/%v", res.DevicePresent, res.ConfPresent, res.DropInPresent)
	}
	if !res.BreakerKnown || res.AppliedHash != "abc123" || res.BreakerBoots != 1 || res.BreakerDisarmed {
		t.Errorf("breaker not parsed: %#v", res)
	}
	if res.LivenessAgeSec == nil || *res.LivenessAgeSec != 7 {
		t.Errorf("liveness age = %v, want 7", res.LivenessAgeSec)
	}
}

func TestParseWatchdogStatus_DisarmedBreaker(t *testing.T) {
	res := ParseWatchdogStatus(
		`breaker={"config_hash":"h","boot_count":3,"disarmed":true,"disarm_reason":"rebooted 3 times under an armed watchdog"}`)

	if !res.BreakerDisarmed || res.BreakerReason == "" || res.BreakerBoots != 3 {
		t.Fatalf("a disarmed breaker must be reported verbatim: %#v", res)
	}
}

func TestParseWatchdogStatus_MissingAndUnreadableFieldsAreUnknown(t *testing.T) {
	// A node with no watchdog ever armed: no breaker file, no liveness file.
	// Every field must read as "we do not know", never as a confident negative —
	// an empty service_state must not be reported as "inactive".
	res := ParseWatchdogStatus(strings.Join([]string{
		"service_state=",
		"service_enabled=",
		"device=absent",
		"conf_present=no",
		"drop_in_present=no",
		"breaker=",
		"liveness_age_sec=",
	}, "\n"))

	if res.ServiceState != "" || res.ServiceEnabled != "" {
		t.Errorf("empty systemd answers must stay empty: %q/%q", res.ServiceState, res.ServiceEnabled)
	}
	if res.BreakerKnown {
		t.Error("an absent breaker file must not read as known")
	}
	if res.LivenessAgeSec != nil {
		t.Errorf("an absent liveness file must leave the age nil, got %v", *res.LivenessAgeSec)
	}
	if res.DevicePresent || res.ConfPresent || res.DropInPresent {
		t.Error("absent files must not report as present")
	}
}

func TestParseWatchdogStatus_GarbageIsIgnored(t *testing.T) {
	// The probe answering partially still tells the operator more than the
	// database does, so a broken line drops that field and nothing else.
	res := ParseWatchdogStatus(strings.Join([]string{
		"this line has no separator",
		"breaker=not json at all",
		"liveness_age_sec=-4",
		"service_state=inactive",
	}, "\n"))

	if res.ServiceState != "inactive" {
		t.Errorf("a good line must survive bad neighbours, got %q", res.ServiceState)
	}
	if res.BreakerKnown {
		t.Error("unparseable breaker JSON must not read as known")
	}
	if res.LivenessAgeSec != nil {
		t.Error("a negative age is not a fact; it must be dropped")
	}
}

func TestWatchdogStatusScanScript_IsReadOnly(t *testing.T) {
	// The probe is a constant, never operator-supplied. If a write ever appears
	// in it, the endpoint stops being a read and its node.read authorization is
	// no longer the right gate.
	for _, forbidden := range []string{"systemctl start", "systemctl stop", "systemctl enable",
		"systemctl disable", "rm ", "> /", "apt-get", "reboot"} {
		if strings.Contains(WatchdogStatusScanScript, forbidden) {
			t.Errorf("the live probe must stay read-only, found %q", forbidden)
		}
	}
}

func TestParseWatchdogStatus_PlatformDisarmIsNotABreakerTrip(t *testing.T) {
	// One agent flag carried both events, so switching the watchdog off — the
	// most ordinary action in the feature — used to report the device's own
	// safety breaker as having fired. That spends the one signal the probe
	// exists to raise.
	byPlatform := ParseWatchdogStatus(
		`breaker={"config_hash":"h","disarmed":true,"disarm_reason":"disabled by the platform","disarm_source":"platform"}`)
	if byPlatform.BreakerDisarmed {
		t.Error("a platform-requested disable must not read as a breaker trip")
	}
	if !byPlatform.DisarmedByPlatform {
		t.Error("it should still be reported as disarmed, just not as a fault")
	}

	// Agents older than the source field only send the reason; the string is
	// the contract that covers them.
	legacyPlatform := ParseWatchdogStatus(
		`breaker={"config_hash":"h","disarmed":true,"disarm_reason":"disabled by the platform"}`)
	if legacyPlatform.BreakerDisarmed {
		t.Error("the legacy platform reason must be recognised without the source field")
	}

	real := ParseWatchdogStatus(
		`breaker={"config_hash":"h","boot_count":3,"disarmed":true,"disarm_reason":"the node rebooted 3 times with the watchdog armed","disarm_source":"breaker"}`)
	if !real.BreakerDisarmed || real.DisarmedByPlatform {
		t.Fatalf("a genuine breaker trip must still be reported: %#v", real)
	}
}

func TestIsPlatformDisarmReason(t *testing.T) {
	if !IsPlatformDisarmReason(PlatformDisarmReason) || !IsPlatformDisarmReason("  disabled by the platform  ") {
		t.Error("the platform reason must be recognised")
	}
	if IsPlatformDisarmReason("the node rebooted 3 times with the watchdog armed") {
		t.Error("a breaker reason is not a platform disarm")
	}
}
