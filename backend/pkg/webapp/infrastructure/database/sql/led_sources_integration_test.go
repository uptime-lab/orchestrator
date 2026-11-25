package sql

import (
	"context"
	"testing"
	"time"
)

// The LED-source advertisement arrives on ConnectionInit and must SURVIVE the
// heartbeats that follow it.
//
// It did not: UpdateAgentInfo is called by three heartbeat/status paths with
// ("", "", nil) purely to stamp last_seen_at, and the advertisement write had
// no guard, so its else-branch NULLed the column roughly 30 seconds after
// every connect: a node that had just advertised four sources read back
// empty, which is why the write is now
// gated on agentVersion, the same discriminator the version and kind writes
// already use.
func TestLEDSourcesSurviveHeartbeats(t *testing.T) {
	repo := agentInstanceRepo(t)
	ctx := context.Background()
	nodeID := seedNodeRow(t, repo)

	advertised := []string{"temperature", "load", "ram", "disk"}
	if err := repo.UpdateAgentInfo(ctx, nodeID, "0.7.1", "full", advertised, time.Now()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	read := func() string {
		var got *string
		if err := repo.db.Raw(`SELECT led_sources FROM nodes WHERE id = ?`, nodeID).Scan(&got).Error; err != nil {
			t.Fatalf("read: %v", err)
		}
		if got == nil {
			return ""
		}
		return *got
	}
	if read() != "temperature,load,ram,disk" {
		t.Fatalf("after connect = %q, want the advertisement stored", read())
	}

	// Three heartbeats, exactly as the gRPC layer issues them.
	for i := 0; i < 3; i++ {
		if err := repo.UpdateAgentInfo(ctx, nodeID, "", "", nil, time.Now()); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}
	if read() != "temperature,load,ram,disk" {
		t.Fatalf("after heartbeats = %q — a heartbeat must not erase what a connect advertised", read())
	}

	// A CONNECT that advertises nothing (a downgrade, or a watcher) still
	// clears it: the column must describe the agent that is actually running.
	if err := repo.UpdateAgentInfo(ctx, nodeID, "0.6.13", "full", nil, time.Now()); err != nil {
		t.Fatalf("downgrade connect: %v", err)
	}
	if read() != "" {
		t.Fatalf("after a non-advertising connect = %q, want cleared", read())
	}
}
