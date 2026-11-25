package node_command

import "testing"

// TestCausesReboot pins which commands take the node down, because the
// consequence of getting it wrong is invisible: a device that is rebooting
// while the UI says ONLINE. Observed on a CM4 flashing its EEPROM:
// update_boot_order reports SUCCESS and only then reboots, so
// nothing but a dispatch-time pin can keep the status honest.
func TestCausesReboot(t *testing.T) {
	for _, slug := range []string{SlugReboot, SlugUpdateBootOrder, SlugEnableEEPROM} {
		if !CausesReboot(slug) {
			t.Errorf("CausesReboot(%q) = false, want true", slug)
		}
	}
	// Shutdown is deliberately NOT here: the node is not coming back on its
	// own, and "restarting" would promise a return that never happens.
	for _, slug := range []string{SlugShutdown, SlugInstallOS, SlugApplyAccessConfig, "run_script", ""} {
		if CausesReboot(slug) {
			t.Errorf("CausesReboot(%q) = true, want false", slug)
		}
	}
}

// The delay bound exists because the agent sleeps the value on its single
// command goroutine and the wire field is a uint32 — a negative float64
// converted into it wraps into a sleep of years.
func TestClampDelaySeconds(t *testing.T) {
	cases := []struct {
		in    float64
		want  float64
		bound bool
	}{
		{0, 0, false},
		{30, 30, false},
		{MaxDelaySeconds, MaxDelaySeconds, false},
		{MaxDelaySeconds + 1, MaxDelaySeconds, true},
		{86400, MaxDelaySeconds, true},
		{-1, 0, true},
	}
	for _, tc := range cases {
		got, bound := ClampDelaySeconds(tc.in)
		if got != tc.want || bound != tc.bound {
			t.Errorf("ClampDelaySeconds(%v) = (%v, %v), want (%v, %v)", tc.in, got, bound, tc.want, tc.bound)
		}
	}
}
