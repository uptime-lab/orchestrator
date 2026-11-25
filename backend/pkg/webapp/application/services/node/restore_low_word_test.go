package node

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// A board flashed again from the browser is stamped with the low word of its
// serial, the eight hex the browser can read. Restoring the deleted record of
// the same board (full serial) would put two live rows for one board in the
// account, so the low-word stamp counts as a holder too. Within one account
// that is safe: the cross-account guard already refuses a second board whose
// low word collides, so a matching stamp here is this board.
func TestRestoreIsRefusedWhenTheBoardIsAPlaceholderStampedWithItsLowWord(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{
		MacAddress:     "2c:cf:67:12:34:56",
		HardwareSerial: serial,
	}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	placeholderID := uuid.New()
	nodes.nodes[placeholderID] = &node.Node{
		ID:          placeholderID,
		AccountID:   accountID,
		Name:        "flashed-from-browser",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "abcd0001"},
	}

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var refusal *SerialBoundInAccountError
	if !errors.As(err, &refusal) || refusal.HolderID != placeholderID {
		t.Fatalf("restore err = %v, want a refusal naming the placeholder %s", err, placeholderID)
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the deleted node's row was written: two live rows now claim one board")
	}
}

// A repeated-nibble low word is shared by unrelated boards, so it is not
// treated as the same board.
func TestRestoreIgnoresADegenerateLowWordStamp(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000ffffffff"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	other := uuid.New()
	nodes.nodes[other] = &node.Node{ID: other, AccountID: accountID, Fingerprint: &node.HardwareFingerprint{HardwareSerial: "ffffffff"}}

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var refusal *SerialBoundInAccountError
	if errors.As(err, &refusal) {
		t.Fatalf("refused over a degenerate low word: %v", err)
	}
}

// The deleted-nodes list says the same, so the panel stops offering a
// restore the restore refuses.
func TestListDeletedReportsALowWordPlaceholderAsTheHolder(t *testing.T) {
	const serial = "10000000abcd0001"
	holderID := uuid.New()
	nodes := &serialScopeFakeRepo{
		scopes:  map[string]repository.SerialBindingScope{},
		holders: map[string]repository.AccountSerialHolder{"abcd0001": {NodeID: holderID, Name: "flashed-from-browser"}},
	}
	svc := listDeletedService(t, nodes, node_tombstone.Tombstone{ID: uuid.New(), HardwareSerial: serial, UniqueToken: "tok"})

	got, err := svc.ListDeleted(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if h := got[0].SerialBoundInAccount; h == nil || h.ID != holderID {
		t.Errorf("SerialBoundInAccount = %+v, want the placeholder %s", h, holderID)
	}
}
