package node

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// A successful install_os writes a new image over the disk, so the marker block
// in config.txt, the rendered watchdog config and the persisted LED state are
// gone — while the recorded hashes still equal the desired ones, which makes
// every ladder read converged and offers no re-apply.
func TestReimageForgetsTheAppliedMarkers(t *testing.T) {
	applied := time.Now().Add(-time.Hour)
	repo := &genFakeRepo{node: &node.Node{
		ID:                       uuid.New(),
		DeviceProfileAppliedHash: "profilehash",
		DeviceProfileAppliedAt:   &applied,
		WatchdogAppliedHash:      "watchdoghash",
		WatchdogDisarmReason:     "device self-disarm",
		LEDAppliedHash:           "ledhash",
	}}
	svc := &Service{nodeRepo: repo, logger: slog.Default()}

	if err := svc.InvalidateAppliedStateAfterReimage(context.Background(), uuid.New(), repo.node.ID); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	for field, got := range map[string]string{
		"device_profile_applied_hash": repo.node.DeviceProfileAppliedHash,
		"watchdog_applied_hash":       repo.node.WatchdogAppliedHash,
		"led_applied_hash":            repo.node.LEDAppliedHash,
		// The disarm reason describes a watchdog that no longer exists on the
		// device; leaving it reports the new OS as having disarmed something it
		// never armed.
		"watchdog_disarm_reason": repo.node.WatchdogDisarmReason,
	} {
		if got != "" {
			t.Errorf("%s = %q after a reimage, want it forgotten", field, got)
		}
	}
	if repo.node.DeviceProfileAppliedAt != nil {
		t.Error("device_profile_applied_at survived the reimage")
	}
}

// A node with nothing recorded must not be written at all: this runs from a
// command settle, and a pointless write bumps updated_at on every install.
func TestReimageWritesNothingWhenNothingWasApplied(t *testing.T) {
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	svc := &Service{nodeRepo: repo, logger: slog.Default()}

	if err := svc.InvalidateAppliedStateAfterReimage(context.Background(), uuid.New(), repo.node.ID); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if repo.columnWrites > 0 {
		t.Fatalf("%d column write(s) for a node with no applied markers", repo.columnWrites)
	}
}
