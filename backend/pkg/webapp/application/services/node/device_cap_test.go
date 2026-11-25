package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
)

// fixedCap is a DeviceCapResolver answering one cap — what a lapsed
// evaluation resolves to (the free cap), or a live subscription's own.
type fixedCap struct {
	cap int
	url string
}

func (f fixedCap) EffectiveDeviceCap(context.Context) (int, string) { return f.cap, f.url }

// capRepo is the slice of the repository the admission path touches, on top
// of the master-token fake: it holds an instance-wide admitted count and
// implements the locked insert the way the SQL repository does — refuse at
// the cap, admit below it, refuse as unverified on a count error.
type capRepo struct {
	*masterTokenConnectRepo
	admitted int64
	// accountAdmitted is the adopting account's own admitted count.
	accountAdmitted int64
	countErr        error
	created         []*node.Node
	capCalls        int
	lastCap         int
	// lastLimits is the full set of limits the last admission was asked
	// under, the account's included.
	lastLimits repository.AdmissionLimits
	plainCall  int
	// placeholder is what the short-serial reconcile lookup finds.
	placeholder *node.Node
	// byID is what Connect and ApproveNode read.
	byID *node.Node
	// admittedUpdates and approvals are the transitions written under the cap.
	admittedUpdates []*node.Node
	approvals       []uuid.UUID
	plainApprovals  int
	// statusWrites and gatedStatusWrites are the status-only writes, plain
	// and under the cap.
	statusWrites      int
	gatedStatusWrites []uuid.UUID
}

func (r *capRepo) GetAwaitingFirstConnectBySerialSuffix(context.Context, uuid.UUID, string) (*node.Node, error) {
	if r.placeholder == nil {
		return nil, app_errors.ErrNodeNotFound
	}
	cp := *r.placeholder
	return &cp, nil
}

func (r *capRepo) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	return r.GetByID(context.Background(), uuid.Nil, uuid.Nil)
}

func (r *capRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	if r.byID == nil {
		return nil, app_errors.ErrNodeNotFound
	}
	cp := *r.byID
	return &cp, nil
}

// gate is the locked check every admission write makes, as the SQL
// repository does it.
func (r *capRepo) gate(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	cap := limits.InstanceCap
	r.capCalls++
	r.lastCap = cap
	r.lastLimits = limits
	if !limits.Bounded() {
		return repository.DeviceCapAdmission{}, nil
	}
	if r.countErr != nil {
		return repository.DeviceCapAdmission{}, &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: r.countErr}
	}
	if cap > 0 && r.admitted >= int64(cap) {
		return repository.DeviceCapAdmission{Count: r.admitted}, &entitlement.DeviceCapError{Cap: cap, Count: r.admitted}
	}
	if limits.Account.Applies() && r.accountAdmitted >= int64(limits.Account.Limit) {
		l := limits.Account
		return repository.DeviceCapAdmission{Count: r.admitted, AccountCount: r.accountAdmitted},
			&entitlement.AccountDeviceLimitError{AccountID: limits.AccountID, Limit: l.Limit, Count: r.accountAdmitted,
				Plan: l.Plan, Until: l.Until, Contact: l.Contact}
	}
	return repository.DeviceCapAdmission{Count: r.admitted, AccountCount: r.accountAdmitted}, nil
}

// admitted1 records one admission in both counts.
func (r *capRepo) admitted1() {
	r.admitted++
	r.accountAdmitted++
}

func (r *capRepo) UpdateUnderDeviceCap(_ context.Context, n *node.Node, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	admission, err := r.gate(limits)
	if err == nil {
		r.admittedUpdates = append(r.admittedUpdates, n)
		r.admitted1()
	}
	return admission, err
}

func (r *capRepo) ApproveUnderDeviceCap(_ context.Context, nodeID uuid.UUID, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	admission, err := r.gate(limits)
	if err == nil {
		r.approvals = append(r.approvals, nodeID)
		r.admitted1()
	}
	return admission, err
}

func (r *capRepo) UpdateApprovalStatus(context.Context, uuid.UUID, string) error {
	r.plainApprovals++
	return nil
}

// GetByHardwareSerial matches on the serial, unlike the embedded fake, so a
// NEW device's handshake reaches the row-creating branch.
func (r *capRepo) GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, serial string) (*node.Node, error) {
	if r.existing == nil || r.existing.Fingerprint == nil || r.existing.Fingerprint.HardwareSerial != serial {
		return nil, app_errors.ErrNodeNotFound
	}
	return r.masterTokenConnectRepo.GetByHardwareSerial(ctx, accountID, serial)
}

func (r *capRepo) CountAdmittedNodes(context.Context) (int64, error) {
	return r.admitted, r.countErr
}

// CountAdmittedNodesForAccount answers the caller's own admitted count,
// which the refusal shows a tenant instead of the install's.
func (r *capRepo) CountAdmittedNodesForAccount(context.Context, uuid.UUID) (int64, error) {
	return r.accountAdmitted, r.countErr
}

func (r *capRepo) CreateUnderDeviceCap(_ context.Context, n *node.Node, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	admission, err := r.gate(limits)
	if err == nil {
		r.created = append(r.created, n)
	}
	return admission, err
}

// Create is the UNGATED insert. Only the restore path may use it.
func (r *capRepo) Create(_ context.Context, n *node.Node) error {
	r.plainCall++
	r.created = append(r.created, n)
	return nil
}

type capHarness struct {
	svc   *Service
	repo  *capRepo
	audit *audittest.Sink
	// masterTokenID is the one master token adoptNew presents.
	masterTokenID uuid.UUID
}

func newCapHarness(existing *node.Node, admitted int64, resolver DeviceCapResolver) *capHarness {
	repo := &capRepo{masterTokenConnectRepo: &masterTokenConnectRepo{existing: existing}, admitted: admitted}
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo:             repo,
		anomalyRepo:          &capturingAnomalyRepo{},
		communicationService: &stubCommunication{},
		deviceCapResolver:    resolver,
		audit:                recorder,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &capHarness{svc: svc, repo: repo, audit: sink, masterTokenID: uuid.New()}
}

func (h *capHarness) adoptNew(t *testing.T, accountID uuid.UUID) (*node.Node, error) {
	t.Helper()
	return h.adoptNewSerial(t, accountID, "10000000deadbeef")
}

// adoptNewSerial presents the harness's one master token with the given
// serial, as a device holding the token would.
func (h *capHarness) adoptNewSerial(t *testing.T, accountID uuid.UUID, serial string) (*node.Node, error) {
	t.Helper()
	return h.svc.ConnectWithMasterToken(context.Background(), h.masterTokenID, accountID,
		node.HardwareFingerprint{HardwareSerial: serial, MacAddress: otherMAC}, services.NodeDefaults{})
}

// An expired evaluation: the resolver has fallen to the free cap while the
// install holds the hundred devices the evaluation admitted. Every one of
// them must keep coming back — the reconnect arm never counts — and only a
// NEW device is refused. This is the property the public promise rests on:
// a lapse stops admissions, never the fleet.
func TestExpiredEvaluationKeepsItsFleet(t *testing.T) {
	accountID := uuid.New()
	existing := admittedNode(accountID)
	h := newCapHarness(existing, 100, fixedCap{cap: entitlement.FreeDeviceCap})

	got, err := h.svc.ConnectWithMasterToken(context.Background(), uuid.New(), accountID,
		node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}, services.NodeDefaults{})
	if err != nil {
		t.Fatalf("an admitted device reconnecting over the lapsed cap was refused: %v", err)
	}
	if got == nil || got.ID != existing.ID {
		t.Fatalf("reconnect returned %+v, want the existing node", got)
	}
	if h.repo.capCalls != 0 {
		t.Errorf("the reconnect arm asked the cap %d time(s); a device already managed is never counted against it", h.repo.capCalls)
	}

	_, err = h.adoptNew(t, accountID)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("the 101st device was not refused at the free cap: %v", err)
	}
	if capErr.Cap != entitlement.FreeDeviceCap || capErr.Count != 100 {
		t.Errorf("refusal says cap %d count %d, want %d and 100", capErr.Cap, capErr.Count, entitlement.FreeDeviceCap)
	}
	if len(h.repo.created) != 0 {
		t.Errorf("a refused admission inserted %d row(s)", len(h.repo.created))
	}
}

// The cap refusal must be its own type all the way out of the service: the
// gRPC layer answers every other master-token refusal with the uniform
// Unauthenticated so a probing device learns nothing, and a cap refusal
// flattened into that would have a device retrying forever against a cap
// only an operator can lift.
func TestMasterTokenAtCapIsDistinguishable(t *testing.T) {
	accountID := uuid.New()
	h := newCapHarness(nil, 25, fixedCap{cap: 25, url: "https://example.com/plans"})

	got, err := h.adoptNew(t, accountID)

	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v, want *entitlement.DeviceCapError through the wrap", err)
	}
	if errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Error("the cap refusal reads as the uniform not-found refusal")
	}
	if got != nil {
		t.Errorf("a refusal returned a node: %+v", got)
	}
	if capErr.SubscriptionURL != "https://example.com/plans" {
		t.Errorf("the refusal does not carry the install's subscription URL: %q", capErr.SubscriptionURL)
	}
	ev, ok := h.audit.Find(audit_event.ActionEntitlementDeviceCapReached)
	if !ok {
		t.Fatalf("no %s audit row; actions: %v", audit_event.ActionEntitlementDeviceCapReached, h.audit.Actions())
	}
	if ev.AccountID != accountID {
		t.Errorf("audit row under account %s, want the adopting account %s", ev.AccountID, accountID)
	}
	if !strings.Contains(ev.Detail, "cap=25") {
		t.Errorf("audit detail %q does not carry the cap", ev.Detail)
	}
}

// The operator's create goes through the same gate, under the operator's
// own actor.
func TestCreateIsRefusedAtTheDeviceCap(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	h := newCapHarness(nil, 30, fixedCap{cap: 30})
	h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}

	_, err := h.svc.Create(context.Background(), node_create.NodeCreateCommand{
		ID: uuid.New(), Name: "one-too-many",
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v, want *entitlement.DeviceCapError", err)
	}
	ev, ok := h.audit.Find(audit_event.ActionEntitlementDeviceCapReached)
	if !ok {
		t.Fatal("no audit row for the refusal")
	}
	if ev.ActorUserID == nil || *ev.ActorUserID != userID {
		t.Errorf("audit actor = %v, want the operator %s", ev.ActorUserID, userID)
	}
}

func TestCreateBelowTheCapAdmits(t *testing.T) {
	h := newCapHarness(nil, 29, fixedCap{cap: 30})
	h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
	n, err := h.svc.Create(context.Background(), node_create.NodeCreateCommand{
		ID: uuid.New(), Name: "fits", AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if err != nil || n == nil {
		t.Fatalf("create below the cap: %v", err)
	}
	if len(h.repo.created) != 1 || h.repo.lastCap != 30 {
		t.Errorf("created %d row(s) under cap %d, want 1 under 30", len(h.repo.created), h.repo.lastCap)
	}
}

// Nothing wired is the FREE cap. A service that forgot the entitlement must
// not be an unlimited one — the public promise is a bound.
func TestNoResolverMeansTheFreeCap(t *testing.T) {
	h := newCapHarness(nil, 25, nil)
	_, err := h.adoptNew(t, uuid.New())
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) || capErr.Cap != entitlement.FreeDeviceCap {
		t.Fatalf("err = %v, want a refusal at the free cap of %d", err, entitlement.FreeDeviceCap)
	}
	if capErr.SubscriptionURL != entitlement.DefaultSubscriptionURL {
		t.Errorf("subscription url = %q, want the default", capErr.SubscriptionURL)
	}
}

// A restore puts back the admission state the node had when it was deleted,
// and only a node that WAS admitted comes back admitted — through the gated
// insert, the same count-and-insert under one lock as any admission. Every
// other shape comes back un-admitted through the plain insert, so its own
// first connect or approval is the admission.
func TestRestoreReturnsTheAdmissionStateAtDeletion(t *testing.T) {
	cases := []struct {
		name         string
		status       node.NodeStatus
		approval     node.ApprovalStatus
		wantStatus   node.NodeStatus
		wantApproval node.ApprovalStatus
		wantAdmitted bool
	}{
		{"created, never connected", node.StatusReadyForAdoption, node.ApprovalStatusApproved,
			node.StatusReadyForAdoption, node.ApprovalStatusApproved, false},
		{"browser flash in progress", node.StatusFlashing, node.ApprovalStatusApproved,
			node.StatusFlashing, node.ApprovalStatusApproved, false},
		{"rejected", node.StatusOffline, node.ApprovalStatusRejected,
			node.StatusOffline, node.ApprovalStatusRejected, false},
		{"pending, last seen online", node.StatusOnline, node.ApprovalStatusPendingApproval,
			node.StatusOffline, node.ApprovalStatusPendingApproval, false},
		{"previously admitted", node.StatusOffline, node.ApprovalStatusApproved,
			node.StatusOffline, node.ApprovalStatusApproved, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, nodes, accountID, nodeID := newScopedCleanupService(t)
			nodes.nodes[nodeID].Status = tc.status
			nodes.nodes[nodeID].ApprovalStatus = tc.approval
			svc.deviceCapResolver = fixedCap{cap: 1}

			if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
				t.Fatalf("delete: %v", err)
			}
			restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
			if err != nil {
				t.Fatalf("restore was refused: %v", err)
			}
			if restored.ID != nodeID || restored.Status != tc.wantStatus || restored.ApprovalStatus != tc.wantApproval {
				t.Errorf("restored id=%s status=%q approval=%q, want id=%s status=%q approval=%q",
					restored.ID, restored.Status, restored.ApprovalStatus, nodeID, tc.wantStatus, tc.wantApproval)
			}
			if restored.IsAdmitted() != tc.wantAdmitted {
				t.Errorf("restored admitted=%t, want %t", restored.IsAdmitted(), tc.wantAdmitted)
			}
			wantLocked, wantPlain := 0, 1
			if tc.wantAdmitted {
				wantLocked, wantPlain = 1, 0
			}
			if nodes.lockedInserts != wantLocked || nodes.plainInserts != wantPlain {
				t.Errorf("inserts: %d locked, %d plain; want %d locked, %d plain",
					nodes.lockedInserts, nodes.plainInserts, wantLocked, wantPlain)
			}
		})
	}
}

// A tombstone written before the admission state was recorded is restored
// NOT admitted (approved, ready_for_adoption), so its device's next connect
// is the admission under the cap. Restoring it admitted would keep the
// bypass open for every record that predates the fix.
func TestRestoreOfALegacyTombstoneIsNotAnAdmission(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	legacy := svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created
	legacy.WasAdmitted, legacy.ApprovalStatus, legacy.Status = nil, "", ""

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.IsAdmitted() || restored.Status != node.StatusReadyForAdoption ||
		restored.ApprovalStatus != node.ApprovalStatusApproved {
		t.Errorf("legacy restore: status=%q approval=%q admitted=%t; want ready_for_adoption, approved, not admitted",
			restored.Status, restored.ApprovalStatus, restored.IsAdmitted())
	}
	if nodes.plainInserts != 1 || nodes.lockedInserts != 0 {
		t.Errorf("legacy restore used %d locked and %d plain inserts; want the plain one", nodes.lockedInserts, nodes.plainInserts)
	}
}

// The cap bounds ADMISSION, and no creating path creates an admitted row:
// the operator's create lands ready_for_adoption, so each create passes the
// courtesy check against a count it never raises. The first connect is
// where the row becomes a device, and that is where the cap must refuse —
// leaving the row exactly as it was, so the device is admitted once a slot
// frees up.
func TestFirstConnectIsTheAdmission(t *testing.T) {
	accountID := uuid.New()
	created := &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
		Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	connect := node_connect.NodeConnectCommand{NodeID: created.ID, AccountID: accountID, UniqueToken: "tok",
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000deadbeef", MacAddress: otherMAC}}

	at := newCapHarness(nil, 25, fixedCap{cap: 25})
	at.repo.byID = created
	got, err := at.svc.Connect(context.Background(), connect)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("first connect at the cap: err = %v, want the cap refusal", err)
	}
	if got != nil || len(at.repo.updates) != 0 || len(at.repo.admittedUpdates) != 0 {
		t.Errorf("a refused first connect wrote the row (%d plain, %d gated updates)", len(at.repo.updates), len(at.repo.admittedUpdates))
	}
	if _, ok := at.audit.Find(audit_event.ActionEntitlementDeviceCapReached); !ok {
		t.Error("the refused first connect was not audited")
	}

	below := newCapHarness(nil, 24, fixedCap{cap: 25})
	below.repo.byID = created
	got, err = below.svc.Connect(context.Background(), connect)
	if err != nil || got == nil {
		t.Fatalf("first connect below the cap: %v", err)
	}
	if len(below.repo.admittedUpdates) != 1 || len(below.repo.updates) != 0 {
		t.Errorf("the admitting write bypassed the cap: %d gated, %d plain", len(below.repo.admittedUpdates), len(below.repo.updates))
	}
}

// Every row created below the cap must not all be able to connect: the
// pre-fix shape (cap on creation only) admitted all of them.
func TestCreatedBelowCapCannotAllConnect(t *testing.T) {
	const n = 5
	h := newCapHarness(nil, 0, fixedCap{cap: n - 1})
	refused := 0
	for i := 0; i < n; i++ {
		accountID := uuid.New()
		h.repo.byID = &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
			Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
		_, err := h.svc.Connect(context.Background(), node_connect.NodeConnectCommand{
			NodeID: h.repo.byID.ID, AccountID: accountID, UniqueToken: "tok",
			Fingerprint: node.HardwareFingerprint{HardwareSerial: "short", MacAddress: otherMAC}})
		var capErr *entitlement.DeviceCapError
		if errors.As(err, &capErr) {
			refused++
		} else if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
	}
	if refused != 1 || h.repo.admitted != n-1 {
		t.Errorf("%d placeholders against a cap of %d: %d refused, %d admitted; want 1 refused, %d admitted",
			n, n-1, refused, h.repo.admitted, n-1)
	}
}

// Approving a pending master-token device admits it: at the cap the
// approval is refused and the row stays pending.
func TestApproveIsTheAdmission(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	pending := &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusOffline,
		ApprovalStatus: node.ApprovalStatusPendingApproval}

	at := newCapHarness(nil, 25, fixedCap{cap: 25})
	at.repo.byID = pending
	_, err := at.svc.ApproveNode(context.Background(), accountID, userID, pending.ID)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("approve at the cap: err = %v, want the cap refusal", err)
	}
	if len(at.repo.approvals) != 0 || at.repo.plainApprovals != 0 {
		t.Error("a refused approval wrote the approval status")
	}
	ev, ok := at.audit.Find(audit_event.ActionEntitlementDeviceCapReached)
	if !ok || ev.ActorUserID == nil || *ev.ActorUserID != userID {
		t.Errorf("the refusal is not audited under the approving operator: %+v", ev)
	}

	below := newCapHarness(nil, 24, fixedCap{cap: 25})
	below.repo.byID = pending
	if _, err := below.svc.ApproveNode(context.Background(), accountID, userID, pending.ID); err != nil {
		t.Fatalf("approve below the cap: %v", err)
	}
	if len(below.repo.approvals) != 1 || below.repo.plainApprovals != 0 {
		t.Errorf("the approval bypassed the cap: %d gated, %d plain", len(below.repo.approvals), below.repo.plainApprovals)
	}
}

// The master-token reconcile into a placeholder is that placeholder's first
// connect. At the cap it is refused — and the handshake must END there:
// falling through to the creating branch would spawn a duplicate row.
func TestPlaceholderReconcileIsTheAdmission(t *testing.T) {
	accountID := uuid.New()
	h := newCapHarness(nil, 25, fixedCap{cap: 25})
	h.repo.placeholder = &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusReadyForAdoption,
		ApprovalStatus: node.ApprovalStatusApproved, AwaitingFirstConnect: true,
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "deadbeef"}}

	_, err := h.adoptNew(t, accountID)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("reconcile at the cap: err = %v, want the cap refusal", err)
	}
	if len(h.repo.created) != 0 || len(h.repo.updates) != 0 || len(h.repo.admittedUpdates) != 0 {
		t.Errorf("a refused reconcile wrote rows: %d created, %d updated", len(h.repo.created), len(h.repo.updates)+len(h.repo.admittedUpdates))
	}
}

// A restored node is counted from its restore and exempt: its device's
// rebind on the next connect is not an admission and never asks the cap.
func TestRestoredNodeRebindIsExemptAtCap(t *testing.T) {
	accountID := uuid.New()
	restored := &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
		Status: node.StatusOffline, ApprovalStatus: node.ApprovalStatusApproved, AwaitingFirstConnect: true,
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: fullSerial}}
	h := newCapHarness(nil, 1000, fixedCap{cap: 1})
	h.repo.byID = restored
	got, err := h.svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: restored.ID,
		AccountID: accountID, UniqueToken: "tok",
		Fingerprint: node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}})
	if err != nil || got == nil {
		t.Fatalf("a restored node's rebind over the cap was refused: %v", err)
	}
	if h.repo.capCalls != 0 {
		t.Errorf("the rebind asked the cap %d time(s)", h.repo.capCalls)
	}
}

func countActions(sink *audittest.Sink, action string) int {
	n := 0
	for _, a := range sink.Actions() {
		if a == action {
			n++
		}
	}
	return n
}

// A refused device asks again on its own, and the audit trail is kept
// forever: a device-driven refusal is recorded once per device per hour,
// while each refusal an operator makes is recorded every time.
func TestDeviceDrivenRefusalIsRecordedOncePerHour(t *testing.T) {
	accountID := uuid.New()
	h := newCapHarness(nil, 25, fixedCap{cap: 25})
	for i := 0; i < 3; i++ {
		if _, err := h.adoptNew(t, accountID); err == nil {
			t.Fatal("adoption at the cap was admitted")
		}
	}
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 1 {
		t.Errorf("three master-token refusals of one device wrote %d audit rows, want 1", n)
	}

	h.repo.byID = &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
		Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	connect := node_connect.NodeConnectCommand{NodeID: h.repo.byID.ID, AccountID: accountID, UniqueToken: "tok",
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "short", MacAddress: otherMAC}}
	for i := 0; i < 3; i++ {
		_, _ = h.svc.Connect(context.Background(), connect)
	}
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 2 {
		t.Errorf("three first-connect refusals of a second device brought the rows to %d, want 2", n)
	}

	// An hour later the same device is recorded again.
	h.svc.capRefusalRecords.mu.Lock()
	for k := range h.svc.capRefusalRecords.last {
		h.svc.capRefusalRecords.last[k] = time.Now().Add(-2 * deviceCapRefusalRecordInterval)
	}
	h.svc.capRefusalRecords.mu.Unlock()
	_, _ = h.svc.Connect(context.Background(), connect)
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 3 {
		t.Errorf("after the interval the device's refusal was not recorded again: %d rows, want 3", n)
	}

	// An operator's refusals are each recorded.
	h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
	for i := 0; i < 2; i++ {
		_, _ = h.svc.Create(context.Background(), node_create.NodeCreateCommand{ID: uuid.New(), Name: "n",
			AuthContext: commands.NewAuthContext(accountID, uuid.New())})
	}
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 5 {
		t.Errorf("two operator refusals brought the rows to %d, want 5", n)
	}
}

// The master-token refusal is throttled per TOKEN: the serial is whatever
// the caller presents, so keyed on it a holder of the token wrote one audit
// row per invented serial. Rotating serials under one token is one row an
// hour; a second token is its own key.
func TestMasterTokenRefusalIsThrottledPerTokenNotPerSerial(t *testing.T) {
	accountID := uuid.New()
	h := newCapHarness(nil, 25, fixedCap{cap: 25})
	for i := 0; i < 5; i++ {
		if _, err := h.adoptNewSerial(t, accountID, fmt.Sprintf("10000000feed%04x", i)); err == nil {
			t.Fatal("adoption at the cap was admitted")
		}
	}
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 1 {
		t.Errorf("five refusals under one master token with five serials wrote %d audit rows, want 1", n)
	}
	h.masterTokenID = uuid.New()
	if _, err := h.adoptNewSerial(t, accountID, "10000000feedffff"); err == nil {
		t.Fatal("adoption at the cap was admitted")
	}
	if n := countActions(h.audit, audit_event.ActionEntitlementDeviceCapReached); n != 2 {
		t.Errorf("a second master token's refusal brought the rows to %d, want 2", n)
	}
}

// CheckDeviceCap is the SSH adoption's pre-flight: the same answer the
// admission would give, without admitting.
func TestCheckDeviceCap(t *testing.T) {
	actor := audit.UserActor(uuid.New(), uuid.New())

	at := newCapHarness(nil, 25, fixedCap{cap: 25})
	var capErr *entitlement.DeviceCapError
	if err := at.svc.CheckDeviceCap(context.Background(), actor); !errors.As(err, &capErr) {
		t.Errorf("at the cap: err = %v, want a refusal", err)
	}
	if _, ok := at.audit.Find(audit_event.ActionEntitlementDeviceCapReached); !ok {
		t.Error("the pre-flight refusal was not audited")
	}

	below := newCapHarness(nil, 24, fixedCap{cap: 25})
	if err := below.svc.CheckDeviceCap(context.Background(), actor); err != nil {
		t.Errorf("below the cap: %v", err)
	}

	unlimited := newCapHarness(nil, 1_000_000, fixedCap{cap: 0})
	unlimited.repo.countErr = errors.New("must not be asked")
	if err := unlimited.svc.CheckDeviceCap(context.Background(), actor); err != nil {
		t.Errorf("unlimited: %v (and it must not even count)", err)
	}

	failing := newCapHarness(nil, 0, fixedCap{cap: 1})
	failing.repo.countErr = errors.New("database is unhappy")
	var unverified *entitlement.DeviceCapUnverifiedError
	if err := failing.svc.CheckDeviceCap(context.Background(), actor); !errors.As(err, &unverified) {
		t.Errorf("a failed count: err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	if errors.As(failing.svc.CheckDeviceCap(context.Background(), actor), &capErr) {
		t.Error("a failed count reads as the cap being reached")
	}
	if _, ok := failing.audit.Find(audit_event.ActionEntitlementDeviceCapReached); ok {
		t.Error("a failed count wrote a cap-reached audit row")
	}
}

// sshKeyRepoWithNoDefault lets Create run keyless, which is the common case.
type sshKeyRepoWithNoDefault struct{ repository.SSHKeyRepository }

func (sshKeyRepoWithNoDefault) GetDefaultByAccountID(context.Context, uuid.UUID) (*ssh_key.SSHKey, error) {
	return nil, app_errors.ErrSSHKeyNotFound
}

// operatorCap is a resolver that also answers the instance-operator question,
// as the entitlement service does.
type operatorCap struct {
	fixedCap
	operator uuid.UUID
}

func (o operatorCap) IsInstanceOperator(_ context.Context, accountID uuid.UUID) bool {
	return accountID == o.operator
}

// The refusal leaving the service says whose
// it is — the account's own admitted count and whether that account operates
// the install — so the HTTP answer can withhold the install-wide count from a
// tenant. Both the locked admission and the SSH-adopt pre-check scope it.
func TestDeviceCapRefusalIsScopedToTheCaller(t *testing.T) {
	operator, tenant := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name     string
		account  uuid.UUID
		resolver DeviceCapResolver
		wantOp   bool
	}{
		{"tenant", tenant, operatorCap{fixedCap{cap: 25}, operator}, false},
		{"operator", operator, operatorCap{fixedCap{cap: 25}, operator}, true},
		{"resolver without the operator question", operator, fixedCap{cap: 25}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCapHarness(nil, 25, tc.resolver)
			h.repo.accountAdmitted = 6

			_, err := h.adoptNew(t, tc.account)
			var capErr *entitlement.DeviceCapError
			if !errors.As(err, &capErr) {
				t.Fatalf("err = %v", err)
			}
			if capErr.AccountID != tc.account || capErr.InstanceOperator != tc.wantOp ||
				!capErr.AccountDevicesKnown || capErr.AccountDevices != 6 || capErr.Count != 25 {
				t.Errorf("admission refusal = %+v", capErr)
			}

			err = h.svc.CheckDeviceCap(context.Background(), audit.UserActor(tc.account, uuid.New()))
			if !errors.As(err, &capErr) {
				t.Fatalf("pre-check err = %v", err)
			}
			if capErr.AccountID != tc.account || capErr.InstanceOperator != tc.wantOp || capErr.AccountDevices != 6 {
				t.Errorf("pre-check refusal = %+v", capErr)
			}
		})
	}
}

// The refusal's audit row lands in the refused account's own trail, which
// that account's super_admin reads. So it is scoped like the HTTP answer: the
// operator's row names the install-wide count, a tenant's row carries the
// cap and its own count and never the install's — on every refusing path.
func TestDeviceCapAuditRowIsScopedToTheCaller(t *testing.T) {
	operator, tenant := uuid.New(), uuid.New()
	resolver := operatorCap{fixedCap{cap: 25}, operator}
	paths := []struct {
		name   string
		refuse func(h *capHarness, accountID uuid.UUID)
	}{
		{"master-token admission", func(h *capHarness, accountID uuid.UUID) { _, _ = h.adoptNew(t, accountID) }},
		{"operator create", func(h *capHarness, accountID uuid.UUID) {
			h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
			_, _ = h.svc.Create(context.Background(), node_create.NodeCreateCommand{ID: uuid.New(), Name: "n",
				AuthContext: commands.NewAuthContext(accountID, uuid.New())})
		}},
		{"ssh-adopt pre-check", func(h *capHarness, accountID uuid.UUID) {
			_ = h.svc.CheckDeviceCap(context.Background(), audit.UserActor(accountID, uuid.New()))
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			h := newCapHarness(nil, 40, resolver)
			h.repo.accountAdmitted = 6
			p.refuse(h, tenant)
			ev, ok := h.audit.Find(audit_event.ActionEntitlementDeviceCapReached)
			if !ok {
				t.Fatal("no refusal audit row")
			}
			if ev.AccountID != tenant {
				t.Fatalf("row under %s, want the tenant", ev.AccountID)
			}
			if strings.Contains(ev.Detail, "count=") || strings.Contains(ev.Detail, "40") {
				t.Errorf("tenant's audit detail %q carries the install-wide count", ev.Detail)
			}
			if !strings.Contains(ev.Detail, "cap=25") || !strings.Contains(ev.Detail, "account_devices=6") {
				t.Errorf("tenant's audit detail %q, want the cap and its own count", ev.Detail)
			}

			h = newCapHarness(nil, 40, resolver)
			h.repo.accountAdmitted = 6
			p.refuse(h, operator)
			ev, ok = h.audit.Find(audit_event.ActionEntitlementDeviceCapReached)
			if !ok {
				t.Fatal("no refusal audit row for the operator")
			}
			if !strings.Contains(ev.Detail, "cap=25") || !strings.Contains(ev.Detail, "count=40") {
				t.Errorf("operator's audit detail %q, want the cap and the install-wide count", ev.Detail)
			}
		})
	}
}

// Deleting an admitted node frees its slot, and the slot may be taken
// before the node is restored. The restore is then an admission like any
// other and is refused at the cap, writing nothing, so no sequence of
// delete, admit and restore takes the admitted count past the cap. Once a
// slot is free the same restore succeeds.
func TestRestoreOfAnAdmittedNodeIsRefusedAtTheCap(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
	other := uuid.New()
	nodes.nodes[other] = &node.Node{ID: other, AccountID: accountID, Status: node.StatusOffline,
		ApprovalStatus: node.ApprovalStatusApproved}
	recorder, sink := audittest.New()
	svc.audit = recorder
	svc.deviceCapResolver = fixedCap{cap: 2}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// The freed slot is taken by a new admission.
	filler := uuid.New()
	nodes.nodes[filler] = &node.Node{ID: filler, AccountID: accountID, Status: node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved}

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("restore into a full install: err = %v, want the device-cap refusal", err)
	}
	if restored != nil {
		t.Errorf("a refused restore returned a node: %+v", restored)
	}
	if _, exists := nodes.nodes[nodeID]; exists {
		t.Error("a refused restore inserted the row")
	}
	if got := nodes.countAdmitted(uuid.Nil); got > 2 {
		t.Errorf("admitted count %d past the cap of 2", got)
	}
	if !svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created.Restorable() {
		t.Error("a refused restore consumed the deleted-node record")
	}
	if _, ok := sink.Find(audit_event.ActionEntitlementDeviceCapReached); !ok {
		t.Error("the refused restore was not audited")
	}

	// A slot frees up: the same record restores, admitted.
	delete(nodes.nodes, filler)
	restored, err = svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore once a slot is free: %v", err)
	}
	if !restored.IsAdmitted() || nodes.countAdmitted(uuid.Nil) != 2 {
		t.Errorf("restored admitted=%t, count %d; want admitted at 2", restored.IsAdmitted(), nodes.countAdmitted(uuid.Nil))
	}
}

// The restore answers with the row as stored, not the value it built: the
// insert is what stamps the timestamps.
func TestRestoreAnswersWithTheStoredRow(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		svc, nodes, accountID, nodeID := newScopedCleanupService(t)
		if admitted {
			nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
		}
		if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
			t.Fatalf("delete: %v", err)
		}
		restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
		if err != nil {
			t.Fatalf("restore (admitted=%t): %v", admitted, err)
		}
		if restored.CreatedAt.IsZero() || restored.UpdatedAt.IsZero() {
			t.Errorf("restore (admitted=%t) answered zero timestamps: created %v updated %v",
				admitted, restored.CreatedAt, restored.UpdatedAt)
		}
	}
}
