package node

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// UpdateStatusPinnedBy is the guarded status write: a move of an approved row
// out of a pre-connect status is an admission, refused here as the SQL
// repository refuses it, so the service retries it under the cap.
func (r *capRepo) UpdateStatusPinnedBy(_ context.Context, nodeID uuid.UUID, status, _ string) error {
	stored := r.byID
	if stored != nil && stored.IsApproved() && node.IsPreConnectStatus(stored.Status) &&
		!node.IsPreConnectStatus(node.NodeStatus(status)) {
		return &node.AdmissionRequiredError{NodeID: nodeID, AccountID: stored.AccountID,
			Stored: stored.Status, Requested: node.NodeStatus(status)}
	}
	r.statusWrites++
	return nil
}

func (r *capRepo) UpdateStatusUnderDeviceCap(_ context.Context, nodeID uuid.UUID, _, _ string, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	admission, err := r.gate(limits)
	if err == nil {
		r.gatedStatusWrites = append(r.gatedStatusWrites, nodeID)
		r.admitted1()
	}
	return admission, err
}

// failingCapHarness is an install with a cap whose lock or count cannot be
// obtained: every admission write the repository is asked for fails the
// check, as the SQL repository answers a failed pg_advisory_xact_lock, a
// failed count or a statement/lock timeout.
func failingCapHarness(cap int) *capHarness {
	h := newCapHarness(nil, 0, fixedCap{cap: cap})
	h.repo.countErr = errors.New("canceling statement due to statement timeout")
	return h
}

// wroteNothing reports every write the fake has seen, admitting or not.
func (h *capHarness) wroteNothing(t *testing.T) {
	t.Helper()
	r := h.repo
	if len(r.created) != 0 || len(r.updates) != 0 || len(r.admittedUpdates) != 0 ||
		len(r.approvals) != 0 || r.plainApprovals != 0 || len(r.gatedStatusWrites) != 0 || r.statusWrites != 0 {
		t.Errorf("a refused admission wrote: %d created, %d updated, %d admitted updates, %d+%d approvals, %d+%d status writes",
			len(r.created), len(r.updates), len(r.admittedUpdates), len(r.approvals), r.plainApprovals,
			len(r.gatedStatusWrites), r.statusWrites)
	}
	if r.admitted != 0 {
		t.Errorf("the admitted count moved to %d", r.admitted)
	}
}

// assertUnverified is the refusal every admission path must give when the
// cap applies and cannot be checked: its own type, never the cap-reached
// one, and no cap-reached audit row.
func assertUnverified(t *testing.T, h *capHarness, err error) {
	t.Helper()
	var unverified *entitlement.DeviceCapUnverifiedError
	if !errors.As(err, &unverified) {
		t.Fatalf("err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	if errors.As(err, new(*entitlement.DeviceCapError)) {
		t.Error("the unverified refusal reads as the cap being reached")
	}
	if _, ok := h.audit.Find(audit_event.ActionEntitlementDeviceCapReached); ok {
		t.Error("an unverified refusal wrote a cap-reached audit row")
	}
}

// A cap that applies and cannot be checked is never lifted: every admission
// path refuses with the unverified error and writes nothing. Admitting
// unchecked would let anyone who can make the count fail — a statement or
// lock timeout on the connection is enough — admit past the cap.
func TestAdmissionIsRefusedWhenTheCapCannotBeChecked(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	created := func() *node.Node {
		return &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
			Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	}
	paths := []struct {
		name  string
		admit func(h *capHarness) (*node.Node, error)
	}{
		{"master-token new device", func(h *capHarness) (*node.Node, error) { return h.adoptNew(t, accountID) }},
		{"master-token placeholder reconcile", func(h *capHarness) (*node.Node, error) {
			h.repo.placeholder = &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusReadyForAdoption,
				ApprovalStatus: node.ApprovalStatusApproved, AwaitingFirstConnect: true,
				Fingerprint: &node.HardwareFingerprint{HardwareSerial: "deadbeef"}}
			return h.adoptNew(t, accountID)
		}},
		{"node-token first connect", func(h *capHarness) (*node.Node, error) {
			h.repo.byID = created()
			return h.svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: h.repo.byID.ID,
				AccountID: accountID, UniqueToken: "tok",
				Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000deadbeef", MacAddress: otherMAC}})
		}},
		{"node-token connect of an approved fingerprinted row", func(h *capHarness) (*node.Node, error) {
			n := created()
			n.Fingerprint = &node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}
			n.MacAddress = realMAC
			h.repo.byID = n
			return h.svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: n.ID,
				AccountID: accountID, UniqueToken: "tok",
				Fingerprint: node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}})
		}},
		{"approval", func(h *capHarness) (*node.Node, error) {
			h.repo.byID = &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusOffline,
				ApprovalStatus: node.ApprovalStatusPendingApproval}
			return h.svc.ApproveNode(context.Background(), accountID, userID, h.repo.byID.ID)
		}},
		{"status write that admits", func(h *capHarness) (*node.Node, error) {
			h.repo.byID = created()
			return nil, h.svc.UpdateStatus(context.Background(), h.repo.byID.ID, string(node.StatusOnline))
		}},
		{"operator create", func(h *capHarness) (*node.Node, error) {
			h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
			return h.svc.Create(context.Background(), node_create.NodeCreateCommand{ID: uuid.New(), Name: "n",
				AuthContext: commands.NewAuthContext(accountID, userID)})
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			h := failingCapHarness(25)
			got, err := p.admit(h)
			assertUnverified(t, h, err)
			if got != nil {
				t.Errorf("a refused admission returned a node: %+v", got)
			}
			if h.repo.capCalls != 1 {
				t.Errorf("the gate was asked %d time(s), want once", h.repo.capCalls)
			}
			h.wroteNothing(t)
		})
	}
}

// The restore of a node that was admitted when deleted is an admission too:
// a failed check refuses it, inserts nothing and leaves the record
// restorable, so the same restore succeeds once the database answers.
func TestRestoreIsRefusedWhenTheCapCannotBeChecked(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
	svc.deviceCapResolver = fixedCap{cap: 25}
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	nodes.capCheckErr = errors.New("lock timeout")

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if !errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		t.Fatalf("restore with an uncheckable cap: err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	if restored != nil {
		t.Errorf("a refused restore returned a node: %+v", restored)
	}
	if _, exists := nodes.nodes[nodeID]; exists || nodes.plainInserts != 0 || nodes.lockedInserts != 0 {
		t.Errorf("a refused restore inserted the row (%d plain, %d locked)", nodes.plainInserts, nodes.lockedInserts)
	}
	if !svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created.Restorable() {
		t.Error("a refused restore consumed the deleted-node record")
	}

	nodes.capCheckErr = nil
	if _, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("restore once the check answers: %v", err)
	}
}

// An unlimited install has no cap to check, so a count that would fail is
// never asked and never refuses: the repository's cap <= 0 is a plain write.
func TestUnlimitedCapAdmitsWhenTheCountWouldFail(t *testing.T) {
	h := newCapHarness(nil, 0, fixedCap{cap: 0})
	h.repo.countErr = errors.New("database is unhappy")
	n, err := h.adoptNew(t, uuid.New())
	if err != nil || n == nil {
		t.Fatalf("admission on an unlimited install: %v", err)
	}
	if len(h.repo.created) != 1 || h.repo.lastCap != 0 {
		t.Errorf("created %d row(s) under cap %d, want 1 under 0", len(h.repo.created), h.repo.lastCap)
	}
	if err := h.svc.CheckDeviceCap(context.Background(), audit.UserActor(uuid.New(), uuid.New())); err != nil {
		t.Errorf("the SSH-adopt pre-check on an unlimited install: %v", err)
	}

	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
	svc.deviceCapResolver = fixedCap{cap: 0}
	nodes.capCheckErr = errors.New("database is unhappy")
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Errorf("restore on an unlimited install: %v", err)
	}
}

// A device the install already manages is never admitted again, so a cap
// that cannot be checked never touches it: the master-token reconnect, the
// node-token reconnect and the status moves inside the admitted set all
// complete without asking the gate. Refusing them would take a fleet offline
// on a database fault.
func TestReconnectOfAnAdmittedNodeNeverAsksTheCap(t *testing.T) {
	accountID := uuid.New()

	h := failingCapHarness(1)
	existing := admittedNode(accountID)
	h.repo.existing = existing
	got, err := h.svc.ConnectWithMasterToken(context.Background(), uuid.New(), accountID,
		node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}, services.NodeDefaults{})
	if err != nil || got == nil || got.ID != existing.ID {
		t.Fatalf("master-token reconnect with an uncheckable cap: node %+v, err %v", got, err)
	}

	admitted := admittedNode(accountID)
	admitted.MacAddress = realMAC
	h.repo.byID = admitted
	got, err = h.svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: admitted.ID,
		AccountID: accountID, UniqueToken: admitted.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}})
	if err != nil || got == nil {
		t.Fatalf("node-token reconnect with an uncheckable cap: %v", err)
	}
	for _, status := range []node.NodeStatus{node.StatusOnline, node.StatusOffline} {
		if err := h.svc.UpdateStatus(context.Background(), admitted.ID, string(status)); err != nil {
			t.Fatalf("status %s of an admitted node with an uncheckable cap: %v", status, err)
		}
	}

	if h.repo.capCalls != 0 {
		t.Errorf("an admitted node's reconnect asked the cap %d time(s)", h.repo.capCalls)
	}
}

// The refusal is one ERROR line per operator action, and once per device per
// interval for a device that keeps asking — never an audit row and never the
// cap-reached WARN.
func TestUnverifiedRefusalIsLoggedAndThrottledPerDevice(t *testing.T) {
	h := failingCapHarness(25)
	var logs bytes.Buffer
	h.svc.logger = slog.New(slog.NewTextHandler(&logs, nil))
	accountID := uuid.New()
	for i := 0; i < 3; i++ {
		if _, err := h.adoptNew(t, accountID); err == nil {
			t.Fatal("an uncheckable admission went ahead")
		}
	}
	if n := strings.Count(logs.String(), "the device cap could not be checked"); n != 1 {
		t.Errorf("three refusals of one device logged %d line(s), want 1:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "failed to create node from master token connection") {
		t.Error("the refusal was logged a second time as a failed create")
	}
	if !strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "at its device cap") {
		t.Errorf("want one ERROR line and no cap-reached line:\n%s", logs.String())
	}
}
