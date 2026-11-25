package node

import "testing"

// The one rule that must not drift: "no information" is Unknown, never
// Changed. Reading an empty side as a restart would fail every below-floor
// agent's in_progress commands instantly on every reconnect.
func TestAgentInstanceContinuityOf(t *testing.T) {
	cases := []struct {
		name              string
		previous, current string
		want              AgentInstanceContinuity
	}{
		{"both empty", "", "", AgentInstanceUnknown},
		{"old agent reconnecting after a new one", "a", "", AgentInstanceUnknown},
		{"first connect of a reporting agent", "", "b", AgentInstanceUnknown},
		{"the same process reconnecting", "a", "a", AgentInstanceSame},
		{"a restarted process", "a", "b", AgentInstanceChanged},
	}
	for _, tc := range cases {
		if got := AgentInstanceContinuityOf(tc.previous, tc.current); got != tc.want {
			t.Errorf("%s: ContinuityOf(%q, %q) = %v, want %v", tc.name, tc.previous, tc.current, got, tc.want)
		}
	}
}
