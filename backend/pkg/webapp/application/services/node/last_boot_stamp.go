package node

import (
	"time"

	node_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// nextLastBootAt decides what the node's boot stamp becomes after a system-info
// report.
//
// The stamp is the PROOF the reboot-completion check runs on: a reboot command
// settles only once the boot moment post-dates its creation, and a node whose
// agent sends no uptime falls back to completing on reconnect — but only while
// the stamp is ABSENT. Preserving a stale stamp through uptime-less reports
// (the old rule) made that fallback unreachable after a downgrade or a reimage
// from an old capture: the frozen stamp never post-dated anything, so every
// later reboot command and the workflow waiting on it hung forever.
//
// Three cases, each deliberate:
//   - a report with a usable uptime REFRESHES the stamp (the ordinary case);
//   - a report whose Boot section carries NO usable uptime CLEARS it — this
//     agent generation cannot maintain the stamp, and no stamp (with its
//     documented complete-on-reconnect fallback) is honest where a frozen one
//     is a hang;
//   - a report with no Boot section at all KEEPS it — a partial report makes
//     no claim about booting either way.
func nextLastBootAt(current *time.Time, boot *node_domain.BootInfo, now time.Time) *time.Time {
	if boot == nil {
		return current
	}
	if bootedAt, ok := node_domain.BootMomentFrom(boot.UptimeSeconds, now); ok {
		return &bootedAt
	}
	return nil
}
