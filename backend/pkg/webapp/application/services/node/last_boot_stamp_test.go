package node

import (
	"testing"
	"time"

	node_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// A stale boot stamp must not be immortal. An agent that stops reporting
// uptime (a downgrade below 0.2.15, or a reimage from an ancient capture)
// could never refresh it, and the complete-on-reconnect fallback keys on the
// stamp being ABSENT — so every later reboot command hung forever.
func TestNextLastBootAt(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)

	t.Run("a usable uptime refreshes the stamp", func(t *testing.T) {
		uptime := int64(120)
		got := nextLastBootAt(&stale, &node_domain.BootInfo{UptimeSeconds: &uptime}, now)
		if got == nil || !got.After(stale) {
			t.Fatalf("stamp = %v, want refreshed from the fresh uptime", got)
		}
	})

	t.Run("a boot section with no uptime clears the stamp", func(t *testing.T) {
		if got := nextLastBootAt(&stale, &node_domain.BootInfo{}, now); got != nil {
			t.Fatalf("stamp = %v, want cleared — a frozen stamp makes the reconnect fallback unreachable and hangs every reboot", got)
		}
	})

	t.Run("a report without a boot section makes no claim", func(t *testing.T) {
		if got := nextLastBootAt(&stale, nil, now); got != &stale {
			t.Fatalf("stamp = %v, want the existing one kept through a partial report", got)
		}
	})
}
