package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// SerialHoldersInAccount answers, like the SQL repository, only from rows of
// the asking account.
func (f *restoreFakeNodeRepo) SerialHoldersInAccount(_ context.Context, accountID uuid.UUID, serials []string) (map[string]repository.AccountSerialHolder, error) {
	if f.serialLookupErr != nil {
		return nil, f.serialLookupErr
	}
	out := make(map[string]repository.AccountSerialHolder)
	for _, want := range serials {
		for _, n := range f.nodes {
			if n.AccountID == accountID && n.Fingerprint != nil && node.NormalizeSerial(n.Fingerprint.HardwareSerial) == want {
				out[want] = repository.AccountSerialHolder{NodeID: n.ID, Name: n.Name}
			}
		}
	}
	return out, nil
}

// The board of a deleted node is adopted again, as a NEW node, into the same
// account. Restoring the old record then put a second live row in that account
// claiming the same hardware serial, and the boot lookup by serial could pick
// the stale one and decide that device's network boot. The restore refuses,
// names the live node, and writes nothing.
func TestRestoringANodeWhoseBoardIsNowAnotherNodeInThisAccountIsRefused(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{
		MacAddress:     "2c:cf:67:12:34:56",
		HardwareSerial: serial,
	}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	readoptedID := uuid.New()
	nodes.nodes[readoptedID] = &node.Node{
		ID:          readoptedID,
		AccountID:   accountID,
		Name:        "bench-pi",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial},
	}

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err == nil {
		t.Fatalf("restore succeeded and returned %+v; want a refusal: the serial is already this account's node %q", restored, "bench-pi")
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the deleted node's row was written despite the refusal: two live rows now claim one serial")
	}
	if len(nodes.nodes) != 1 {
		t.Errorf("%d live rows after the refused restore, want only the re-adopted node", len(nodes.nodes))
	}
	var refusal *SerialBoundInAccountError
	if !errors.As(err, &refusal) {
		t.Fatalf("restore error = %v (%T), want a *SerialBoundInAccountError so the handler answers it as itself", err, err)
	}
	if refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "serial_bound_in_account" {
		t.Errorf("answered %d/%s, want 409/serial_bound_in_account", refusal.HTTPStatus(), refusal.ErrorCode())
	}
	if refusal.HolderID != readoptedID {
		t.Errorf("refusal names node %s, want the re-adopted node %s", refusal.HolderID, readoptedID)
	}
	for _, want := range []string{`"bench-pi"`, "this account", "delete that node first"} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("message %q does not contain %q", refusal.Error(), want)
		}
	}
	if svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created.RestoredAt != nil {
		t.Error("the record was stamped restored by a refused restore")
	}
}

// Held in this account AND in another: the own-account holder is the one to
// act on, and naming it is safe (it is the caller's own node). The other
// account is never named.
func TestASerialHeldHereAndElsewhereIsRefusedNamingOnlyTheOwnNode(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	own, foreign := uuid.New(), uuid.New()
	nodes.nodes[own] = &node.Node{ID: own, AccountID: accountID, Name: "bench-pi",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial}}
	nodes.nodes[foreign] = &node.Node{ID: foreign, AccountID: uuid.New(), Name: "their-node",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial}}

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var refusal *SerialBoundInAccountError
	if !errors.As(err, &refusal) {
		t.Fatalf("restore error = %v (%T), want a *SerialBoundInAccountError", err, err)
	}
	if strings.Contains(refusal.Error(), "their-node") || strings.Contains(refusal.Error(), foreign.String()) {
		t.Errorf("the refusal names another account's node: %q", refusal.Error())
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the node row was written despite the refusal")
	}
}

// A failed lookup is not a refusal naming a node; the restore stops with the
// cause and writes nothing.
func TestARestoreWhoseOwnAccountLookupFailsIsNotReportedAsBound(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	nodes.serialLookupErr = errors.New("connection reset by peer")

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err == nil {
		t.Fatal("restore succeeded while the serial could not be checked")
	}
	var refusal *SerialBoundInAccountError
	if errors.As(err, &refusal) {
		t.Errorf("a failed lookup was reported as a same-account binding: %v", err)
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the node row was written although the serial could not be checked")
	}
}

// A short stamp or a degenerate serial is legitimately shared, so another node
// of the account carrying the same one does not block the restore.
func TestASharedShortSerialInThisAccountDoesNotBlockTheRestore(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const short = "abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: short}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	other := uuid.New()
	nodes.nodes[other] = &node.Node{ID: other, AccountID: accountID, Name: "other",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: short}}

	if _, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("restore of a record with a shared short serial: %v", err)
	}
}

// The panel must say so before the operator presses Restore: the record is
// not restorable while its serial is another live node of this account.
func TestListDeletedReportsASerialHeldByAnotherNodeOfThisAccount(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	readoptedID := uuid.New()
	nodes.nodes[readoptedID] = &node.Node{
		ID: readoptedID, AccountID: accountID, Name: "bench-pi",
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial},
	}
	record := *svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created
	scopes := &serialScopeFakeRepo{
		scopes:  map[string]repository.SerialBindingScope{serial: {InAccount: true}},
		holders: map[string]repository.AccountSerialHolder{serial: {NodeID: readoptedID, Name: "bench-pi"}},
	}
	list := listDeletedService(t, scopes, record)

	got, err := list.ListDeleted(context.Background(), accountID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	raw, err := json.Marshal(results.NewDeletedNodeListResult(got))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		DeletedNodes []map[string]any `json:"deleted_nodes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	row := body.DeletedNodes[0]
	if row["restorable"] != false {
		t.Errorf("restorable = %v, want false while the serial is this account's live node", row["restorable"])
	}
	if row["serial_bound_elsewhere"] != false {
		t.Errorf("serial_bound_elsewhere = %v, want false: it keeps meaning ANOTHER account", row["serial_bound_elsewhere"])
	}
	holder, _ := row["serial_bound_in_account"].(map[string]any)
	if holder["node_id"] != readoptedID.String() || holder["name"] != "bench-pi" {
		t.Errorf("serial_bound_in_account = %v, want the live node %s %q", row["serial_bound_in_account"], readoptedID, "bench-pi")
	}
}
