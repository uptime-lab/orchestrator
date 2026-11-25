package node

import (
	"context"
	"fmt"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// ReleaseUnclaimedSerialLeases frees the hardware serial of every node that was
// never admitted and has shown no life since unseenSince, and records each
// release in the owning account's audit trail.
//
// Why a lease at all: every per-device secret in this system is minted BY the
// binding, so a first bind has no proof of possession and none can be added
// without a physical provisioning step. What can be made true instead is that
// a binding nobody has ever presented hardware for does not last: holding a
// serial one does not own then costs a heartbeating device, which is
// attributable, instead of one authenticated POST, which is free. The row is
// kept — the placeholder is still the operator's — and the device that really
// carries the serial binds it on its next connect through the ordinary
// first-bind path.
//
// Best-effort per row after the release itself: the audit write and the
// node-updated push must not undo or block a release that has happened.
func (s *Service) ReleaseUnclaimedSerialLeases(ctx context.Context, unseenSince time.Time) (int, error) {
	released, err := s.nodeRepo.ReleaseUnclaimedSerialLeases(ctx, unseenSince)
	if err != nil {
		return 0, fmt.Errorf("releasing unclaimed serial leases: %w", err)
	}
	for _, r := range released {
		s.logger.InfoContext(ctx, "released the serial of a never-admitted node past its lease",
			"node_id", r.NodeID, "account_id", r.AccountID, "serial", r.HardwareSerial)
		s.audit.RecordDetail(ctx, audit.SystemActor(r.AccountID),
			audit_event.ActionNodeSerialLeaseReleased, audit_event.TargetNode, r.NodeID.String(),
			fmt.Sprintf("serial=%q unseen_since=%s", r.HardwareSerial, unseenSince.UTC().Format(time.RFC3339)))
		if updated, err := s.nodeRepo.GetByID(ctx, r.AccountID, r.NodeID); err == nil && updated != nil {
			s.notifyNodeUpdated(*updated)
		}
	}
	return len(released), nil
}
