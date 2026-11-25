package node

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Approving a pending device admits it into the fleet, and rejecting one
// refuses it; both are operator decisions recorded under the member who made
// them, against the node.
func TestApprovalDecisionsAreAuditedUnderTheOperator(t *testing.T) {
	for _, tc := range []struct {
		action string
		decide func(h *capHarness, accountID, userID, nodeID uuid.UUID) error
	}{
		{audit_event.ActionNodeApprove, func(h *capHarness, accountID, userID, nodeID uuid.UUID) error {
			_, err := h.svc.ApproveNode(context.Background(), accountID, userID, nodeID)
			return err
		}},
		{audit_event.ActionNodeReject, func(h *capHarness, accountID, userID, nodeID uuid.UUID) error {
			_, err := h.svc.RejectNode(context.Background(), accountID, userID, nodeID)
			return err
		}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			accountID, userID := uuid.New(), uuid.New()
			pending := &node.Node{ID: uuid.New(), AccountID: accountID, Name: "pending-1",
				Status: node.StatusOffline, ApprovalStatus: node.ApprovalStatusPendingApproval}
			h := newCapHarness(nil, 0, fixedCap{cap: 25})
			h.repo.byID = pending

			if err := tc.decide(h, accountID, userID, pending.ID); err != nil {
				t.Fatalf("decision: %v", err)
			}
			ev, ok := h.audit.Find(tc.action)
			if !ok {
				t.Fatalf("no %s row; actions = %v", tc.action, h.audit.Actions())
			}
			if ev.ActorUserID == nil || *ev.ActorUserID != userID {
				t.Fatalf("actor = %v, want the operator %s", ev.ActorUserID, userID)
			}
			if ev.TargetType != audit_event.TargetNode || ev.TargetID != pending.ID.String() {
				t.Fatalf("target = %s/%s, want node/%s", ev.TargetType, ev.TargetID, pending.ID)
			}
		})
	}
}
