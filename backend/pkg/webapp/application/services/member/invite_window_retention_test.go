package member

import (
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// The invite rate limit is counted off audit_events. The retention sweep
// never keeps that table shorter than audit_event.MinRetentionDays, so the
// window must fit inside the floor or the sweep deletes the rows that bound it.
func TestInviteWindowFitsTheAuditRetentionFloor(t *testing.T) {
	floor := time.Duration(audit_event.MinRetentionDays) * 24 * time.Hour
	if inviteRateWindow > floor {
		t.Fatalf("the invite window (%s) is longer than the audit retention floor (%s): raise audit_event.MinRetentionDays",
			inviteRateWindow, floor)
	}
}
