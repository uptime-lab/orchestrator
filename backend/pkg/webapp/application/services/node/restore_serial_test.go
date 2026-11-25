package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// HTTP-boot resolves a booting device ONLY by `fingerprint->>'hardware_serial'`,
// so a restore that returns the row without the serial leaves a netboot-
// dependent device unable to come back: the ROM asks for its per-serial
// bootstrap, the platform still finds no node, and a fail-closed boot path
// 404s and the device falls through to a disk that may not boot. No agent
// starts, so nothing rebinds the fingerprint, and the loop closes on a device
// recoverable only by physical access.
//
// It looks like it works because a device still RUNNING rebinds its own
// fingerprint over gRPC on the next connect, which is the path a test on live
// hardware takes.
func TestRestoreRebindsTheHardwareSerial(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{
		MacAddress:     "2c:cf:67:12:34:56",
		HardwareSerial: serial,
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if restored.Fingerprint == nil || restored.Fingerprint.HardwareSerial != serial {
		t.Fatalf("restored fingerprint = %+v, want the serial %q back", restored.Fingerprint, serial)
	}
	// The fingerprint carries no MAC and Connect compares one, so without this
	// the device is refused over gRPC as well — locked out both ways.
	if !restored.AwaitingFirstConnect {
		t.Error("the restored node does not await its first connect, so a MAC mismatch will refuse the device")
	}
}

// A serial freed by a delete can be bound by anyone — a tombstone's serial is
// invisible to ListByExactSerial — so an account move routinely leaves the old
// account's tombstone naming a serial another account now owns.
//
// Restoring it used to log a warning and hand back a row with NO fingerprint:
// the operator is answered 200, the panel shows the node back, and the device is
// unreachable forever (HTTP-boot resolves by serial alone, and Connect refuses
// the device on first bind). The restore must refuse, and say which serial
// and what to do about it.
func TestRestoringANodeWhoseSerialMovedIsRefused(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{
		MacAddress:     "2c:cf:67:12:34:56",
		HardwareSerial: serial,
	}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The device is adopted into a different account while this one is deleted.
	foreignID := uuid.New()
	nodes.nodes[foreignID] = &node.Node{
		ID:          foreignID,
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial},
	}

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err == nil {
		t.Fatalf("restore succeeded and returned %+v; want a refusal, because the device can never reach that row", restored)
	}

	var refusal *SerialBoundElsewhereError
	if !errors.As(err, &refusal) {
		t.Fatalf("restore error = %v (%T), want a *SerialBoundElsewhereError so the handler answers it as itself", err, err)
	}
	if refusal.HTTPStatus() != http.StatusConflict {
		t.Errorf("status = %d, want 409: the request is fine and the serial's current owner refuses it", refusal.HTTPStatus())
	}
	if !strings.Contains(refusal.Error(), serial) {
		t.Errorf("message %q does not name the serial the operator is holding", refusal.Error())
	}

	// The row must NOT be back. A restore that half-succeeded is the defect.
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the node row was created despite the refusal, which is the ghost row this test exists to prevent")
	}
}

// The restore is a bind like any other, so the low word is guarded too: a
// serial ending in another account's short stamp would put the restored row
// beside that account's device on the eight-hex path its EEPROM asks for.
func TestRestoringANodeWhoseLowWordAnotherAccountHoldsIsRefused(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	stampID := uuid.New()
	nodes.nodes[stampID] = &node.Node{
		ID:          stampID,
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "abcd0001"},
	}

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var refusal *SerialBoundElsewhereError
	if !errors.As(err, &refusal) {
		t.Fatalf("restore error = %v (%T), want a *SerialBoundElsewhereError", err, err)
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the node row was created despite the refusal")
	}
}

// A lookup that FAILED is not a conflict. Reporting it as one sends the operator
// to argue with a tenant who is holding nothing, over a database hiccup.
func TestARestoreWhoseSerialLookupFailsIsNotReportedAsAConflict(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	nodes.serialLookupErr = errors.New("connection reset by peer")

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err == nil {
		t.Fatal("restore succeeded while the serial could not be checked; it must not bind a serial it could not verify")
	}
	var refusal *SerialBoundElsewhereError
	if errors.As(err, &refusal) {
		t.Errorf("a failed lookup was reported as a cross-account conflict: %v", err)
	}
}

// A node deleted before it ever reported hardware has no serial to give back,
// and inventing one would be worse than leaving it empty.
func TestRestoreWithoutASerialIsStillARestore(t *testing.T) {
	svc, _, accountID, nodeID := newScopedCleanupService(t)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Fingerprint != nil {
		t.Errorf("restored fingerprint = %+v, want none for a node that never reported hardware", restored.Fingerprint)
	}
	if restored.AwaitingFirstConnect {
		t.Error("a node with no serial should not be marked awaiting-first-connect: there is nothing to rebind against")
	}
}

// serialScopeFakeRepo answers the projection the list path uses, and records
// what it was asked, so a test can assert the caller filtered its input.
type serialScopeFakeRepo struct {
	repository.NodeRepository
	scopes  map[string]repository.SerialBindingScope
	holders map[string]repository.AccountSerialHolder
	err     error
	asked   []string
}

func (f *serialScopeFakeRepo) SerialHoldersInAccount(_ context.Context, _ uuid.UUID, serials []string) (map[string]repository.AccountSerialHolder, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]repository.AccountSerialHolder)
	for _, s := range serials {
		if h, ok := f.holders[s]; ok {
			out[s] = h
		}
	}
	return out, nil
}

func (f *serialScopeFakeRepo) SerialBindingScopes(_ context.Context, _ uuid.UUID, serials []string) (map[string]repository.SerialBindingScope, error) {
	f.asked = append(f.asked, serials...)
	if f.err != nil {
		return nil, f.err
	}
	return f.scopes, nil
}

func listDeletedService(t *testing.T, nodes repository.NodeRepository, tombstones ...node_tombstone.Tombstone) *Service {
	t.Helper()
	return &Service{
		nodeRepo:      nodes,
		tombstoneRepo: &listFakeTombstoneRepo{rows: tombstones},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type listFakeTombstoneRepo struct {
	repository.NodeTombstoneRepository
	rows []node_tombstone.Tombstone
}

func (f *listFakeTombstoneRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]node_tombstone.Tombstone, error) {
	return f.rows, nil
}

// The panel must be able to stop offering a restore that the restore path will
// refuse. Without the resolution the flag is always false and the panel keeps
// promising "restoring is enough, it rejoins on its own".
func TestListDeletedFlagsASerialTakenByAnotherAccount(t *testing.T) {
	const moved, mine, short = "10000000abcd0001", "10000000aaaa0001", "abcd0001"
	nodes := &serialScopeFakeRepo{scopes: map[string]repository.SerialBindingScope{
		moved: {InAnotherAccount: true},
		mine:  {InAccount: true},
	}}
	svc := listDeletedService(t, nodes,
		node_tombstone.Tombstone{HardwareSerial: moved},
		node_tombstone.Tombstone{HardwareSerial: mine},
		node_tombstone.Tombstone{HardwareSerial: short},
		node_tombstone.Tombstone{HardwareSerial: ""},
	)

	got, err := svc.ListDeleted(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !got[0].SerialBoundElsewhere {
		t.Error("the serial another account carries is not flagged, so the panel still offers a restore that will be refused")
	}
	if got[1].SerialBoundElsewhere {
		t.Error("this account's OWN serial was flagged; the restore would rebind it happily")
	}
	if got[2].SerialBoundElsewhere || got[3].SerialBoundElsewhere {
		t.Error("a short or absent serial was flagged, which hides the recovery affordance for a healthy device")
	}
	// Serials the guard never acts on must not even be asked about: querying them
	// is wasted work and any answer would be the wrong one.
	for _, asked := range nodes.asked {
		if asked == short || asked == "" {
			t.Errorf("looked up %q, which IsUniqueFullSerial excludes", asked)
		}
	}
}

// A serial this account also holds is not "bound elsewhere": that field keeps
// meaning ANOTHER account for existing clients. The restore is refused all the
// same — the account's own live node holds the serial — and the record says
// which node, in its own field.
func TestListDeletedReportsAnOwnAccountHolderApartFromAnotherAccount(t *testing.T) {
	const serial = "10000000abcd0001"
	holderID := uuid.New()
	nodes := &serialScopeFakeRepo{
		scopes:  map[string]repository.SerialBindingScope{serial: {InAccount: true, InAnotherAccount: true}},
		holders: map[string]repository.AccountSerialHolder{serial: {NodeID: holderID, Name: "bench-pi"}},
	}
	svc := listDeletedService(t, nodes, node_tombstone.Tombstone{ID: uuid.New(), HardwareSerial: serial, UniqueToken: "tok"})

	got, err := svc.ListDeleted(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got[0].SerialBoundElsewhere {
		t.Error("flagged as bound elsewhere a serial this account also holds")
	}
	if h := got[0].SerialBoundInAccount; h == nil || h.ID != holderID || h.Name != "bench-pi" {
		t.Errorf("SerialBoundInAccount = %+v, want the live node %s", h, holderID)
	}
}

// A RESTORED record's serial is carried by the restored node itself, under the
// record's own id: that is not a conflict, and the record is not offered anyway.
func TestListDeletedDoesNotReportARestoredRecordAsHeldByItself(t *testing.T) {
	const serial = "10000000abcd0001"
	id := uuid.New()
	restoredAt := time.Now()
	nodes := &serialScopeFakeRepo{
		scopes:  map[string]repository.SerialBindingScope{serial: {InAccount: true}},
		holders: map[string]repository.AccountSerialHolder{serial: {NodeID: id, Name: "self"}},
	}
	svc := listDeletedService(t, nodes,
		node_tombstone.Tombstone{ID: id, HardwareSerial: serial, UniqueToken: "tok", RestoredAt: &restoredAt},
		node_tombstone.Tombstone{ID: id, HardwareSerial: serial, UniqueToken: "tok"},
	)

	got, err := svc.ListDeleted(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range got {
		if got[i].SerialBoundInAccount != nil {
			t.Errorf("record %d reported as held by its own id: %+v", i, got[i].SerialBoundInAccount)
		}
	}
}

// A database hiccup must not empty or redden the panel: it is the only route
// back from a delete, and the restore re-asks the question and refuses on its
// own, so nothing here is load-bearing for correctness.
func TestListDeletedFallsOpenWhenTheSerialLookupFails(t *testing.T) {
	nodes := &serialScopeFakeRepo{err: errors.New("connection reset by peer")}
	svc := listDeletedService(t, nodes, node_tombstone.Tombstone{HardwareSerial: "10000000abcd0001"})

	got, err := svc.ListDeleted(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("a lookup failure emptied the panel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want the list returned unchanged", len(got))
	}
	if got[0].SerialBoundElsewhere {
		t.Error("flagged a record whose serial could not be checked; unknown must read as not-flagged")
	}
}

// A node whose token was unreadable when it was deleted leaves a record with
// no credential. Restoring it wrote an empty unique_token — no device can
// present one, and a second such restore collided with the first and
// answered 500. It is refused, typed, before anything is written.
func TestRestoringARecordWithNoReadableCredentialIsRefused(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].UniqueToken = ""
	nodes.nodes[nodeID].UniqueTokenUnreadable = true
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
		var refusal *NodeCredentialUnreadableError
		if !errors.As(err, &refusal) {
			t.Fatalf("attempt %d: err = %v (%T), want a *NodeCredentialUnreadableError", attempt, err, err)
		}
		if refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "node_credential_unreadable" {
			t.Errorf("answered %d/%s, want 409/node_credential_unreadable", refusal.HTTPStatus(), refusal.ErrorCode())
		}
		if !strings.Contains(refusal.Error(), "adopt the device again") {
			t.Errorf("message %q does not tell the operator what to do", refusal.Error())
		}
		if _, ok := nodes.nodes[nodeID]; ok {
			t.Fatalf("attempt %d: the node row was written with no credential", attempt)
		}
	}
}

// A record whose ciphertext no configured key can read is the recoverable
// case: same type and code, with the key named as the remedy.
func TestRestoringARecordWhoseCredentialCannotBeDecryptedNamesTheKey(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	record := svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created
	record.UniqueToken, record.UniqueTokenUnreadable = "", true

	_, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var refusal *NodeCredentialUnreadableError
	if !errors.As(err, &refusal) || !refusal.KeyProblem || refusal.HTTPStatus() != http.StatusConflict {
		t.Fatalf("err = %v, want a 409 credential refusal naming the key", err)
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Error("the node row was written with no credential")
	}
}
