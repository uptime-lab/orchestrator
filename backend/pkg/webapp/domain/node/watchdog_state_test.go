package node

import "testing"

// The flag answers one question: if this watchdog reboots the node in a loop,
// does anything bring the node back to the platform? That needs BOTH an armed
// boot-loop failsafe (positive effective threshold) AND network-first boot —
// a node that boots its disk directly never passes through the failsafe.
func TestWatchdogRecoveryLimited(t *testing.T) {
	cases := []struct {
		name         string
		enabled      bool
		threshold    int
		networkFirst bool
		want         bool
	}{
		{"disabled config is never flagged", false, 0, false, false},
		{"disabled config is never flagged even with recovery", false, 3, true, false},
		{"no failsafe, no netboot", true, 0, false, true},
		{"failsafe armed but node boots its disk directly", true, 3, false, true},
		{"netboot but the failsafe is off", true, 0, true, true},
		{"failsafe armed and network-first: recovery exists", true, 3, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WatchdogRecoveryLimited(tc.enabled, tc.threshold, tc.networkFirst); got != tc.want {
				t.Errorf("WatchdogRecoveryLimited(%v, %d, %v) = %v, want %v",
					tc.enabled, tc.threshold, tc.networkFirst, got, tc.want)
			}
		})
	}
}

// The per-node override wins over the global default in both directions:
// enabling on a disabled install and disabling on an enabled one. The same
// resolution drives the netboot withhold path, so the two must agree.
func TestEffectiveBootFailsafeThreshold(t *testing.T) {
	intp := func(v int) *int { return &v }
	cases := []struct {
		name     string
		override *int
		global   int
		want     int
	}{
		{"no override falls back to the global default", nil, 3, 3},
		{"no override, global disabled", nil, 0, 0},
		{"override wins over the global", intp(5), 3, 5},
		{"override 0 disables even when the global is on", intp(0), 3, 0},
		{"override enables on a disabled install", intp(2), 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &Node{BootFailsafeThreshold: tc.override}
			if got := n.EffectiveBootFailsafeThreshold(tc.global); got != tc.want {
				t.Errorf("EffectiveBootFailsafeThreshold(%d) = %d, want %d", tc.global, got, tc.want)
			}
		})
	}
}
