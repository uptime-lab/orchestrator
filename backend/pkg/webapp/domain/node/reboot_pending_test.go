package node

import (
	"testing"
	"time"
)

func ptrInt64(v int64) *int64 { return &v }

// The boot moment must come from uptime, not from the device's wall clock.
// A node with a blank hardware RTC boots into the year 2000 (measured), and a
// boot moment read from that clock sits before every apply forever — the
// "reboot still owed" answer inverted, permanently.
func TestBootMomentFromUsesUptimeNotTheDeviceClock(t *testing.T) {
	observed := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	got, ok := BootMomentFrom(ptrInt64(3600), observed)
	if !ok {
		t.Fatal("an uptime of an hour must yield a boot moment")
	}
	if want := observed.Add(-time.Hour); !got.Equal(want) {
		t.Fatalf("boot moment: got %s want %s", got, want)
	}
}

// Unknown must stay distinguishable from "just booted": an agent that reports
// no uptime would otherwise resolve to observedAt, i.e. "booted right now",
// which silently clears a reboot that is genuinely owed.
func TestBootMomentFromRefusesAbsentUptime(t *testing.T) {
	observed := time.Now()
	for _, u := range []*int64{nil, ptrInt64(0), ptrInt64(-5)} {
		if _, ok := BootMomentFrom(u, observed); ok {
			t.Fatalf("uptime %v must not produce a boot moment", u)
		}
	}
}

func TestDeviceProfileRebootPending(t *testing.T) {
	applied := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	before := applied.Add(-2 * time.Hour)
	after := applied.Add(2 * time.Minute)

	cases := []struct {
		name       string
		appliedAt  *time.Time
		lastBootAt *time.Time
		want       *bool
	}{
		{"booted before the apply — reboot owed", &applied, &before, boolPtr(true)},
		{"booted after the apply — nothing owed", &applied, &after, boolPtr(false)},
		{"never applied — unknown", nil, &after, nil},
		{"never reported an uptime — unknown", &applied, nil, nil},
		{"neither — unknown", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{DeviceProfileAppliedAt: c.appliedAt, LastBootAt: c.lastBootAt}
			got := n.DeviceProfileRebootPending()
			switch {
			case c.want == nil && got != nil:
				t.Fatalf("expected unknown, got %v", *got)
			case c.want != nil && got == nil:
				t.Fatalf("expected %v, got unknown", *c.want)
			case c.want != nil && *got != *c.want:
				t.Fatalf("got %v want %v", *got, *c.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// Same tri-state contract as the device-profile check: unknown must stay
// unknown, because a caller that reads nil as "pending" would pin "reboot
// required" on every node that never flashed, and one that reads it as "not
// pending" would hide the state the column exists to keep.
func TestBootloaderRebootPending(t *testing.T) {
	flashed := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	before := flashed.Add(-2 * time.Hour)
	after := flashed.Add(2 * time.Minute)

	cases := []struct {
		name       string
		flashedAt  *time.Time
		lastBootAt *time.Time
		want       *bool
	}{
		{"booted before the flash — reboot owed", &flashed, &before, boolPtr(true)},
		{"booted after the flash — nothing owed", &flashed, &after, boolPtr(false)},
		{"never flashed — unknown", nil, &after, nil},
		{"never reported an uptime — unknown", &flashed, nil, nil},
		{"neither — unknown", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{BootloaderFlashedAt: c.flashedAt, LastBootAt: c.lastBootAt}
			got := n.BootloaderRebootPending()
			switch {
			case c.want == nil && got != nil:
				t.Fatalf("expected unknown, got %v", *got)
			case c.want != nil && got == nil:
				t.Fatalf("expected %v, got unknown", *c.want)
			case c.want != nil && *got != *c.want:
				t.Fatalf("got %v want %v", *got, *c.want)
			}
		})
	}
}

// ReportedUptimeSeconds must survive every shape of a partially-populated
// system info rather than panicking on a node that has only just connected.
func TestReportedUptimeSecondsHandlesMissingLayers(t *testing.T) {
	if (&Node{}).ReportedUptimeSeconds() != nil {
		t.Fatal("no system info must be nil")
	}
	if (&Node{SystemInfo: &SystemInfo{}}).ReportedUptimeSeconds() != nil {
		t.Fatal("no hardware must be nil")
	}
	if (&Node{SystemInfo: &SystemInfo{Hardware: &HardwareInfo{}}}).ReportedUptimeSeconds() != nil {
		t.Fatal("no boot info must be nil")
	}
	n := &Node{SystemInfo: &SystemInfo{Hardware: &HardwareInfo{Boot: &BootInfo{UptimeSeconds: ptrInt64(42)}}}}
	if got := n.ReportedUptimeSeconds(); got == nil || *got != 42 {
		t.Fatalf("uptime: %v", got)
	}
}
