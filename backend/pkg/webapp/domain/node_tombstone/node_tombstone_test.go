package node_tombstone

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// RestoreState never yields an admitted row unless the record says the node
// WAS admitted: every record it did not write itself, or cannot read, comes
// back as a placeholder whose next connect is the admission.
func TestRestoreStateAdmitsOnlyWhatWasAdmitted(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name         string
		t            Tombstone
		wantStatus   node.NodeStatus
		wantApproval node.ApprovalStatus
	}{
		{"admitted", Tombstone{WasAdmitted: &yes, ApprovalStatus: node.ApprovalStatusApproved, Status: node.StatusOnline},
			node.StatusOffline, node.ApprovalStatusApproved},
		{"placeholder", Tombstone{WasAdmitted: &no, ApprovalStatus: node.ApprovalStatusApproved, Status: node.StatusReadyForAdoption},
			node.StatusReadyForAdoption, node.ApprovalStatusApproved},
		{"rejected while online", Tombstone{WasAdmitted: &no, ApprovalStatus: node.ApprovalStatusRejected, Status: node.StatusOnline},
			node.StatusOffline, node.ApprovalStatusRejected},
		{"approved, not admitted, impossible status", Tombstone{WasAdmitted: &no, ApprovalStatus: node.ApprovalStatusApproved, Status: node.StatusOnline},
			node.StatusReadyForAdoption, node.ApprovalStatusApproved},
		{"recorded with an unknown approval", Tombstone{WasAdmitted: &no, ApprovalStatus: "", Status: node.StatusOffline},
			node.StatusReadyForAdoption, node.ApprovalStatusApproved},
		{"legacy record", Tombstone{},
			node.StatusReadyForAdoption, node.ApprovalStatusApproved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, approval := tc.t.RestoreState()
			if status != tc.wantStatus || approval != tc.wantApproval {
				t.Errorf("RestoreState() = %q/%q, want %q/%q", status, approval, tc.wantStatus, tc.wantApproval)
			}
			admitted := (&node.Node{Status: status, ApprovalStatus: approval}).IsAdmitted()
			wantAdmitted := tc.t.WasAdmitted != nil && *tc.t.WasAdmitted
			if admitted != wantAdmitted {
				t.Errorf("restored row admitted=%t, want %t", admitted, wantAdmitted)
			}
		})
	}
}
