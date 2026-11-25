package image

import (
	"testing"

	"github.com/google/uuid"
)

func TestNeedsIdentityStripFor(t *testing.T) {
	source := uuid.New()
	other := uuid.New()

	backup := NewCaptureImage(uuid.New(), uuid.New(), "backup-x", false, source, "node-x")
	if backup.Kind != KindBackup {
		t.Fatalf("kind = %q, want backup", backup.Kind)
	}
	if backup.NeedsIdentityStripFor(source) {
		t.Error("restoring a backup onto its source node must not strip identity")
	}
	if !backup.NeedsIdentityStripFor(other) {
		t.Error("restoring a backup onto another node must strip identity")
	}

	golden := NewCaptureImage(uuid.New(), uuid.New(), "golden-x", true, source, "node-x")
	if golden.Kind != KindGolden {
		t.Fatalf("kind = %q, want golden", golden.Kind)
	}
	if golden.NeedsIdentityStripFor(other) {
		t.Error("golden images are generalized at capture; no strip needed")
	}

	plain := NewImage(uuid.New(), "ubuntu", "24.04", "", "")
	if plain.NeedsIdentityStripFor(other) {
		t.Error("uploaded OS images never carry a node identity")
	}
	if plain.EffectiveKind() != KindOS {
		t.Errorf("EffectiveKind = %q, want os", plain.EffectiveKind())
	}

	// Legacy capture rows (backfilled kind, unknown source) fail safe: strip.
	legacy := &Image{Kind: KindBackup}
	if !legacy.NeedsIdentityStripFor(other) {
		t.Error("a backup with unknown source must strip identity")
	}
}
