package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The property under test: a master token plus a serial must
// never be enough to be handed an ADMITTED node's identity. An admitted node
// with a bound fingerprint is identified by that whole fingerprint, and a live
// stream means the real device is present — either way the caller is refused
// and the stored fingerprint is left alone.
//
// The legitimate flows that must keep working are tested alongside: the
// reimage-from-master-SD of an OFFLINE node (same fingerprint, no write), the
// WebUSB placeholder (approved, awaiting first connect, no MAC yet), the
// tombstone restore (same shape) and the pending node.

// masterTokenConnectRepo is the slice of the repository the existing-node arm
// touches. The embedded interface is nil, so anything else panics rather than
// silently returning a zero value.
type masterTokenConnectRepo struct {
	repository.NodeRepository
	existing *node.Node
	updates  []*node.Node
}

func (r *masterTokenConnectRepo) GetByHardwareSerial(context.Context, uuid.UUID, string) (*node.Node, error) {
	if r.existing == nil {
		return nil, app_errors.ErrNodeNotFound
	}
	// A copy, the way a real repository hands back a fresh row: a refusal that
	// mutated the returned struct must not be able to leak into r.existing.
	cp := *r.existing
	if r.existing.Fingerprint != nil {
		fp := *r.existing.Fingerprint
		cp.Fingerprint = &fp
	}
	return &cp, nil
}

// ListByExactSerial backs guardSerialBinding for a full serial: the only owner
// is the caller's own account, so the guard passes.
func (r *masterTokenConnectRepo) ListByExactSerial(_ context.Context, serial string) ([]*node.Node, error) {
	if r.existing != nil && r.existing.Fingerprint != nil && node.NormalizeSerial(r.existing.Fingerprint.HardwareSerial) == serial {
		return []*node.Node{r.existing}, nil
	}
	return nil, nil
}

// LowWordHolderInAnotherAccount: this fake models one account, so nothing
// collides.
func (r *masterTokenConnectRepo) LowWordHolderInAnotherAccount(context.Context, uuid.UUID, string) (*repository.SerialHolder, error) {
	return nil, nil
}

func (r *masterTokenConnectRepo) Update(_ context.Context, n *node.Node) error {
	r.updates = append(r.updates, n)
	return nil
}

type capturingAnomalyRepo struct {
	repository.InstanceAnomalyRepository
	recorded []*instance_anomaly.Anomaly
}

func (r *capturingAnomalyRepo) Record(_ context.Context, a *instance_anomaly.Anomaly) error {
	r.recorded = append(r.recorded, a)
	return nil
}

type stubCommunication struct {
	service.NodeCommunicationService
	connected bool
}

func (c *stubCommunication) IsNodeConnected(string) bool { return c.connected }

const (
	fullSerial = "10000000abcd0001"
	realMAC    = "2c:cf:67:12:34:56"
	otherMAC   = "2c:cf:67:ab:cd:ef"
)

func admittedNode(accountID uuid.UUID) *node.Node {
	return &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		UniqueToken:    "n-token",
		ApprovalStatus: node.ApprovalStatusApproved,
		Status:         node.StatusOffline,
		Fingerprint:    &node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC},
	}
}

type masterTokenHarness struct {
	svc  *Service
	repo *masterTokenConnectRepo
	anom *capturingAnomalyRepo
	comm *stubCommunication
}

func newMasterTokenHarness(existing *node.Node, connected bool) *masterTokenHarness {
	h := &masterTokenHarness{
		repo: &masterTokenConnectRepo{existing: existing},
		anom: &capturingAnomalyRepo{},
		comm: &stubCommunication{connected: connected},
	}
	h.svc = &Service{
		nodeRepo:             h.repo,
		anomalyRepo:          h.anom,
		communicationService: h.comm,
		logger:               slog.New(slog.DiscardHandler),
	}
	return h
}

func (h *masterTokenHarness) connect(t *testing.T, accountID uuid.UUID, fp node.HardwareFingerprint) (*node.Node, error) {
	t.Helper()
	return h.svc.ConnectWithMasterToken(context.Background(), uuid.New(), accountID, fp, services.NodeDefaults{})
}

func (h *masterTokenHarness) assertRefusedUntouched(t *testing.T, got *node.Node, err error) {
	t.Helper()
	if !errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Fatalf("err = %v, want ErrNodeNotFound (the uniform refusal the gRPC layer answers)", err)
	}
	if got != nil {
		t.Errorf("a refusal returned a node row: %+v", got)
	}
	if len(h.repo.updates) != 0 {
		t.Errorf("the fingerprint was written on a refusal (%d update(s)); the real device's next Connect would now fail", len(h.repo.updates))
	}
	if len(h.anom.recorded) != 1 {
		t.Fatalf("recorded %d anomalies, want exactly one", len(h.anom.recorded))
	}
	a := h.anom.recorded[0]
	if a.Kind != instance_anomaly.KindMasterTokenIdentityMismatch {
		t.Errorf("anomaly kind = %q, want %q", a.Kind, instance_anomaly.KindMasterTokenIdentityMismatch)
	}
	if a.AccountID == nil {
		t.Errorf("anomaly is not scoped to the account")
	}
}

// The exploit: same serial, a different MAC, on an admitted node that is
// offline. Before the fix this rewrote the fingerprint and returned the row,
// and the gRPC layer then handed over the node's permanent token.
func TestMasterTokenConnectRefusesAdmittedNodeWithDifferentMAC(t *testing.T) {
	accountID := uuid.New()
	h := newMasterTokenHarness(admittedNode(accountID), false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: otherMAC})

	h.assertRefusedUntouched(t, got, err)
}

// The reimage-from-master-SD flow: the device that IS the node comes back on
// the master token while offline — otherwise an expired master token strands
// a node. Same serial, same MAC: the row is returned so the gRPC layer can
// restore the identity, and nothing is written because nothing changed.
func TestMasterTokenConnectReturnsOfflineAdmittedNodeWithIdenticalFingerprint(t *testing.T) {
	accountID := uuid.New()
	existing := admittedNode(accountID)
	h := newMasterTokenHarness(existing, false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC})

	if err != nil {
		t.Fatalf("err = %v, want nil: the device that is the node must be able to come back", err)
	}
	if got == nil || got.ID != existing.ID {
		t.Fatalf("got %+v, want the existing node %s", got, existing.ID)
	}
	if len(h.repo.updates) != 0 {
		t.Errorf("an unchanged fingerprint was written (%d update(s))", len(h.repo.updates))
	}
	if len(h.anom.recorded) != 0 {
		t.Errorf("a legitimate reconnect recorded %d anomalies", len(h.anom.recorded))
	}
}

// The amplification: the lookup is allowed to land on a node by serial SUFFIX
// (the WebUSB short-serial fallback), so a caller who knows only the tail of a
// serial reaches an admitted node's row. The stored serial and the submitted
// one must be equal exactly, the same rule node-token Connect applies.
func TestMasterTokenConnectRefusesSuffixLandedAdmittedNode(t *testing.T) {
	accountID := uuid.New()
	h := newMasterTokenHarness(admittedNode(accountID), false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: "abcd0001", MacAddress: realMAC})

	h.assertRefusedUntouched(t, got, err)
}

// A live stream means the real device is present right now. Even an identical
// fingerprint is refused then: a second device claiming the same hardware while
// the first is connected is not a reimage, and admitting it would displace the
// live stream and redeliver its queued commands to the newcomer.
func TestMasterTokenConnectRefusesWhenTheNodeHoldsALiveStream(t *testing.T) {
	accountID := uuid.New()
	h := newMasterTokenHarness(admittedNode(accountID), true)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC})

	h.assertRefusedUntouched(t, got, err)
}

// A WebUSB placeholder is approved, stamped with the SHORT serial, carries no
// MAC and awaits its first connect. The first device to arrive on that serial
// binds the full fingerprint — that is what the placeholder is for.
func TestMasterTokenConnectRebindsApprovedPlaceholderWithoutMAC(t *testing.T) {
	accountID := uuid.New()
	placeholder := &node.Node{
		ID:                   uuid.New(),
		AccountID:            accountID,
		UniqueToken:          "n-token",
		ApprovalStatus:       node.ApprovalStatusApproved,
		AwaitingFirstConnect: true,
		Fingerprint:          &node.HardwareFingerprint{HardwareSerial: "abcd0001"},
	}
	h := newMasterTokenHarness(placeholder, false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: otherMAC})

	if err != nil {
		t.Fatalf("err = %v, want nil: the placeholder exists to be bound by the first device", err)
	}
	if got == nil || got.ID != placeholder.ID {
		t.Fatalf("got %+v, want the placeholder %s", got, placeholder.ID)
	}
	if len(h.repo.updates) != 1 || h.repo.updates[0].Fingerprint == nil ||
		h.repo.updates[0].Fingerprint.MacAddress != otherMAC ||
		h.repo.updates[0].Fingerprint.HardwareSerial != fullSerial {
		t.Errorf("the placeholder was not rebound to the reported fingerprint: %d update(s)", len(h.repo.updates))
	}
	if len(h.anom.recorded) != 0 {
		t.Errorf("a placeholder bind recorded %d anomalies", len(h.anom.recorded))
	}
}

// A restored tombstone recreates the node with the FULL serial, no MAC and
// AwaitingFirstConnect set (tombstone.go), and must rebind on the next connect
// whatever MAC the device now reports — this is the operator's path for a
// genuine hardware change: Delete, then Restore.
func TestMasterTokenConnectRebindsRestoredNodeAwaitingFirstConnect(t *testing.T) {
	accountID := uuid.New()
	restored := admittedNode(accountID)
	restored.AwaitingFirstConnect = true
	restored.Fingerprint = &node.HardwareFingerprint{HardwareSerial: fullSerial}
	h := newMasterTokenHarness(restored, false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: otherMAC})

	if err != nil {
		t.Fatalf("err = %v, want nil: a restored node rebinds on its next connect", err)
	}
	if got == nil || got.ID != restored.ID {
		t.Fatalf("got %+v, want the restored node %s", got, restored.ID)
	}
	if len(h.repo.updates) != 1 || h.repo.updates[0].Fingerprint == nil || h.repo.updates[0].Fingerprint.MacAddress != otherMAC {
		t.Errorf("the restored node was not rebound: %d update(s)", len(h.repo.updates))
	}
}

// The rebind of a restored node is ONE bind, not an open door: the write that
// binds the new fingerprint also ends the wait for a first connect, so the
// next master-token handshake presenting the same serial with another MAC is
// refused like any claim on an admitted, bound node.
func TestMasterTokenConnectRestoredNodeIsBoundAfterItsFirstRebind(t *testing.T) {
	accountID := uuid.New()
	restored := admittedNode(accountID)
	restored.AwaitingFirstConnect = true
	restored.Fingerprint = &node.HardwareFingerprint{HardwareSerial: fullSerial}
	h := newMasterTokenHarness(restored, false)

	if _, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}); err != nil {
		t.Fatalf("first connect: err = %v, want nil", err)
	}
	if len(h.repo.updates) != 1 {
		t.Fatalf("first connect wrote %d update(s), want 1", len(h.repo.updates))
	}
	bound := h.repo.updates[0]
	if bound.AwaitingFirstConnect {
		t.Fatal("the rebind left the row awaiting its first connect; every later handshake with the serial can rebind it again")
	}

	// The row as persisted, and the real device now holding its stream.
	h.repo.existing = bound
	h.repo.updates = nil
	h.comm.connected = true

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: otherMAC})
	h.assertRefusedUntouched(t, got, err)
}

// A node still awaiting approval holds nothing worth taking — the gRPC layer
// withholds the identity from it — and its fingerprint may still settle, so
// today's behaviour stands: the fingerprint follows the device.
func TestMasterTokenConnectKeepsPendingNodeBehaviour(t *testing.T) {
	accountID := uuid.New()
	pending := admittedNode(accountID)
	pending.ApprovalStatus = node.ApprovalStatusPendingApproval
	h := newMasterTokenHarness(pending, false)

	got, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: otherMAC})

	if err != nil {
		t.Fatalf("err = %v, want nil for a pending node", err)
	}
	if got == nil || got.ID != pending.ID {
		t.Fatalf("got %+v, want the pending node %s", got, pending.ID)
	}
	if len(h.repo.updates) != 1 {
		t.Errorf("pending node's fingerprint was not updated: %d update(s)", len(h.repo.updates))
	}
	if len(h.anom.recorded) != 0 {
		t.Errorf("a pending-node reconnect recorded %d anomalies", len(h.anom.recorded))
	}
}

// A rejected node stays rejected whatever fingerprint arrives — the check that
// was already there, kept ahead of the identity guard.
func TestMasterTokenConnectStillRefusesRejectedNode(t *testing.T) {
	accountID := uuid.New()
	rejected := admittedNode(accountID)
	rejected.ApprovalStatus = node.ApprovalStatusRejected
	h := newMasterTokenHarness(rejected, false)

	_, err := h.connect(t, accountID, node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC})

	if !errors.Is(err, app_errors.ErrNodeRejected) {
		t.Fatalf("err = %v, want ErrNodeRejected", err)
	}
}
