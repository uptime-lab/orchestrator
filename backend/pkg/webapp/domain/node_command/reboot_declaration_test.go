package node_command

import "testing"

// Absence must mean "unknown", never "no". An agent that predates the field
// keeps the dispatch-time pin — which exists precisely because the agent cannot
// be trusted to report a reboot it is about to perform.
func TestAgentDeclinedReboot(t *testing.T) {
	cases := []struct {
		name   string
		result map[string]string
		want   bool
	}{
		{"explicit no", map[string]string{"rebooting": "false"}, true},
		{"explicit yes", map[string]string{"rebooting": "true"}, false},
		{"older agent sends nothing", map[string]string{}, false},
		{"no result at all", nil, false},
		{"unrecognised value is not a no", map[string]string{"rebooting": "maybe"}, false},
	}
	for _, tc := range cases {
		if got := AgentDeclinedReboot(tc.result); got != tc.want {
			t.Errorf("%s: AgentDeclinedReboot(%v) = %v, want %v", tc.name, tc.result, got, tc.want)
		}
	}
	// The slug is still a reboot-class command; only the per-run report differs.
	if !CausesReboot(SlugEnableEEPROM) {
		t.Error("enable_eeprom must stay a reboot-class slug: it does reboot a CM4")
	}
}
