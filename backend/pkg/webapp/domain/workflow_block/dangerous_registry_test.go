package workflow_block

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// Dangerous ⊂ Privileged held by coincidence and was untested. This pin
// turns the coincidence into a decision:
// today the frontend's only reaction to Dangerous is a red border and the word
// "danger" — the run confirmation is a bare "Start playbook X on N nodes?" —
// so the privileged-authoring gate is the ONE control that keeps a
// destructive block from being authored and run by a low role. Until a real
// dangerous-run confirmation exists in the UI, a Dangerous block that is not
// privileged is destructive OS work behind nothing at all.
//
// Relaxing this is allowed, but it is a decision with a prerequisite: ship the
// UI confirmation (and decide which roles may pass it) FIRST, then delete this
// test in the same change and say so. Marking a genuinely manager-safe block
// Dangerous just for the red border is the one shape this test refuses.
func TestEveryDangerousBlockIsPrivileged(t *testing.T) {
	sawDangerous := false
	for _, def := range Registry() {
		if !def.Dangerous {
			continue
		}
		sawDangerous = true
		if !authz.IsPrivilegedBlock(def.Type) {
			t.Errorf("block %q is Dangerous but not privileged — the UI shows no "+
				"real confirmation for Dangerous, so privileged authoring is the only "+
				"gate in front of it; see the comment on this test before relaxing",
				def.Type)
		}
	}
	if !sawDangerous {
		t.Fatal("no Dangerous block in the registry — the invariant this test pins is empty")
	}
}
