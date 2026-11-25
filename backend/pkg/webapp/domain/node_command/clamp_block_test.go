package node_command

import "testing"

// The blocks that WAIT are bounded for the same reason the reboot delay is: the
// agent runs them on its single command goroutine. The clamp lives before the
// int32 conversion on purpose — that conversion saturates on arm64 (a 68-year
// sleep) and wraps negative on amd64, so an unclamped value wedges a node on
// one server architecture and does nothing on the other.
func TestClampBlockSeconds(t *testing.T) {
	cases := []struct {
		name      string
		in        float64
		want      float64
		wantBound bool
	}{
		{"unset is left alone — each handler has its own default", 0, 0, false},
		{"a normal poll interval passes", 10, 10, false},
		{"a long but legitimate wait passes", 1800, 1800, false},
		{"the ceiling itself is not bound", MaxBlockSeconds, MaxBlockSeconds, false},
		{"one past the ceiling binds", MaxBlockSeconds + 1, MaxBlockSeconds, true},
		{"a day binds", 86400, MaxBlockSeconds, true},
		{"a negative binds to zero rather than wrapping through the uint", -5, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bound := ClampBlockSeconds(tc.in)
			if got != tc.want || bound != tc.wantBound {
				t.Errorf("ClampBlockSeconds(%v) = (%v, %v), want (%v, %v)", tc.in, got, bound, tc.want, tc.wantBound)
			}
		})
	}
}

// Both ceilings answer the same question — "how long may one command hold the
// agent's only command goroutine" — so they are the same number, and a change
// to one is a decision about the other.
func TestBlockAndDelayCeilingsAgree(t *testing.T) {
	if MaxBlockSeconds != MaxDelaySeconds {
		t.Fatalf("MaxBlockSeconds = %d, MaxDelaySeconds = %d — both bound the same goroutine", MaxBlockSeconds, MaxDelaySeconds)
	}
}
