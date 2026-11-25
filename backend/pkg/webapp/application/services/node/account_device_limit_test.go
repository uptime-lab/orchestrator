package node

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// fixedAccountLimit is an AccountDeviceLimitResolver answering one limit for
// every account, or an error.
type fixedAccountLimit struct {
	limit entitlement.AccountLimit
	err   error
	calls *int
}

func (f fixedAccountLimit) AccountDeviceLimit(context.Context, uuid.UUID) (entitlement.AccountLimit, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.limit, f.err
}

func freeLimit(n int) fixedAccountLimit {
	return fixedAccountLimit{limit: entitlement.AccountLimit{Limit: n, Plan: entitlement.AccountLimitPlanFree,
		Contact: "sales@example.com"}}
}

// accountHarness is the cap harness on an install whose instance cap is
// unlimited (as on a hosted install), with the account already holding own
// admitted devices and a per-account limit.
func accountHarness(own int64, limit AccountDeviceLimitResolver) *capHarness {
	h := newCapHarness(nil, own, fixedCap{cap: 0})
	h.repo.accountAdmitted = own
	h.svc.SetAccountDeviceLimitResolver(limit)
	return h
}

func wantAccountLimitRefusal(t *testing.T, err error, limit int, count int64) *entitlement.AccountDeviceLimitError {
	t.Helper()
	var limitErr *entitlement.AccountDeviceLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("err = %v, want *entitlement.AccountDeviceLimitError", err)
	}
	if errors.As(err, new(*entitlement.DeviceCapError)) {
		t.Fatal("the account-limit refusal reads as the instance cap's")
	}
	if limitErr.Limit != limit || limitErr.Count != count {
		t.Errorf("refusal says %d of %d, want %d of %d", limitErr.Count, limitErr.Limit, count, limit)
	}
	return limitErr
}

// The first connect of a node waiting for its device is an admission: at the
// account's limit it is refused and the row is left as it was; below it,
// admitted through the gated write, with the account's limit in the gate.
func TestFirstConnectIsRefusedAtTheAccountLimit(t *testing.T) {
	accountID := uuid.New()
	created := &node.Node{ID: uuid.New(), AccountID: accountID, UniqueToken: "tok",
		Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	connect := node_connect.NodeConnectCommand{NodeID: created.ID, AccountID: accountID, UniqueToken: "tok",
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000deadbeef", MacAddress: otherMAC}}

	at := accountHarness(5, freeLimit(5))
	at.repo.byID = created
	got, err := at.svc.Connect(context.Background(), connect)
	limitErr := wantAccountLimitRefusal(t, err, 5, 5)
	if limitErr.Contact != "sales@example.com" {
		t.Errorf("the refusal does not carry the contact: %q", limitErr.Contact)
	}
	if got != nil || len(at.repo.updates) != 0 || len(at.repo.admittedUpdates) != 0 {
		t.Errorf("a refused first connect wrote the row")
	}
	ev, ok := at.audit.Find(audit_event.ActionEntitlementAccountDeviceLimitReached)
	if !ok {
		t.Fatalf("no %s row; actions %v", audit_event.ActionEntitlementAccountDeviceLimitReached, at.audit.Actions())
	}
	if ev.AccountID != accountID || !strings.Contains(ev.Detail, "limit=5") || !strings.Contains(ev.Detail, "account_devices=5") {
		t.Errorf("audit row = %+v", ev)
	}
	if _, ok := at.audit.Find(audit_event.ActionEntitlementDeviceCapReached); ok {
		t.Error("an account-limit refusal was recorded as the instance cap's")
	}

	below := accountHarness(4, freeLimit(5))
	below.repo.byID = created
	if got, err := below.svc.Connect(context.Background(), connect); err != nil || got == nil {
		t.Fatalf("first connect below the account limit: %v", err)
	}
	if len(below.repo.admittedUpdates) != 1 {
		t.Errorf("the admitting write bypassed the gate: %d gated", len(below.repo.admittedUpdates))
	}
	if l := below.repo.lastLimits; l.AccountID != accountID || l.Account.Limit != 5 {
		t.Errorf("the gate was asked without the account's limit: %+v", l)
	}
}

// Approving a pending device admits it, under the account's limit too.
func TestApproveIsRefusedAtTheAccountLimit(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	pending := &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusOffline,
		ApprovalStatus: node.ApprovalStatusPendingApproval}

	at := accountHarness(3, freeLimit(3))
	at.repo.byID = pending
	_, err := at.svc.ApproveNode(context.Background(), accountID, userID, pending.ID)
	wantAccountLimitRefusal(t, err, 3, 3)
	if len(at.repo.approvals) != 0 || at.repo.plainApprovals != 0 {
		t.Error("a refused approval wrote the approval status")
	}
	ev, ok := at.audit.Find(audit_event.ActionEntitlementAccountDeviceLimitReached)
	if !ok || ev.ActorUserID == nil || *ev.ActorUserID != userID {
		t.Errorf("the refusal is not audited under the approving operator: %+v", ev)
	}

	below := accountHarness(2, freeLimit(3))
	below.repo.byID = pending
	if _, err := below.svc.ApproveNode(context.Background(), accountID, userID, pending.ID); err != nil {
		t.Fatalf("approve below the account limit: %v", err)
	}
	if len(below.repo.approvals) != 1 {
		t.Errorf("%d gated approvals, want 1", len(below.repo.approvals))
	}
}

// The master-token reconcile into a placeholder is its first connect; at
// the account's limit the handshake ends without writing anything.
func TestPlaceholderReconcileIsRefusedAtTheAccountLimit(t *testing.T) {
	accountID := uuid.New()
	placeholder := func() *node.Node {
		return &node.Node{ID: uuid.New(), AccountID: accountID, Status: node.StatusReadyForAdoption,
			ApprovalStatus: node.ApprovalStatusApproved, AwaitingFirstConnect: true,
			Fingerprint: &node.HardwareFingerprint{HardwareSerial: "deadbeef"}}
	}
	h := accountHarness(5, freeLimit(5))
	h.repo.placeholder = placeholder()
	_, err := h.adoptNew(t, accountID)
	wantAccountLimitRefusal(t, err, 5, 5)
	if len(h.repo.created) != 0 || len(h.repo.updates) != 0 || len(h.repo.admittedUpdates) != 0 {
		t.Error("a refused reconcile wrote rows")
	}

	below := accountHarness(4, freeLimit(5))
	below.repo.placeholder = placeholder()
	if _, err := below.adoptNew(t, accountID); err != nil {
		t.Fatalf("reconcile below the account limit: %v", err)
	}
	if len(below.repo.admittedUpdates) != 1 {
		t.Errorf("%d gated updates, want 1", len(below.repo.admittedUpdates))
	}
}

// The restore of a node admitted when it was deleted is an admission under
// the account's limit as well: refused while the account is full, the record
// stays restorable and restores once a slot is free.
func TestRestoreOfAnAdmittedNodeIsRefusedAtTheAccountLimit(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	nodes.nodes[nodeID].ApprovalStatus = node.ApprovalStatusApproved
	recorder, sink := audittest.New()
	svc.audit = recorder
	svc.deviceCapResolver = fixedCap{cap: 0}
	svc.SetAccountDeviceLimitResolver(freeLimit(1))

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	filler := uuid.New()
	nodes.nodes[filler] = &node.Node{ID: filler, AccountID: accountID, Status: node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved}

	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	wantAccountLimitRefusal(t, err, 1, 1)
	if restored != nil {
		t.Errorf("a refused restore returned a node")
	}
	if _, exists := nodes.nodes[nodeID]; exists {
		t.Error("a refused restore inserted the row")
	}
	if !svc.tombstoneRepo.(*restoreFakeTombstoneRepo).created.Restorable() {
		t.Error("a refused restore consumed the deleted-node record")
	}
	if _, ok := sink.Find(audit_event.ActionEntitlementAccountDeviceLimitReached); !ok {
		t.Error("the refused restore was not audited")
	}

	delete(nodes.nodes, filler)
	if restored, err = svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("restore once a slot is free: %v", err)
	}
	if !restored.IsAdmitted() {
		t.Error("the restore below the limit did not admit the node")
	}
}

// An override raises and lowers the bound: the same account at the same
// count is admitted under a subscription of 50 and refused under one of 5,
// whatever the install's default.
func TestAccountOverrideRaisesAndLowersTheLimit(t *testing.T) {
	until := time.Now().Add(182 * 24 * time.Hour)
	raised := fixedAccountLimit{limit: entitlement.AccountLimit{Limit: 50, Plan: entitlement.AccountLimitPlanSubscription, Until: &until}}
	lowered := fixedAccountLimit{limit: entitlement.AccountLimit{Limit: 5, Plan: entitlement.AccountLimitPlanSubscription}}
	create := func(h *capHarness) error {
		h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
		_, err := h.svc.Create(context.Background(), node_create.NodeCreateCommand{
			ID: uuid.New(), Name: "n", AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
		})
		return err
	}
	if err := create(accountHarness(12, raised)); err != nil {
		t.Errorf("12 devices under a subscription of 50 were refused: %v", err)
	}
	limitErr := wantAccountLimitRefusal(t, create(accountHarness(6, lowered)), 5, 6)
	if limitErr.Plan != entitlement.AccountLimitPlanSubscription {
		t.Errorf("the refusal names plan %q, want subscription", limitErr.Plan)
	}
	details := wantAccountLimitRefusal(t, create(accountHarness(50, raised)), 50, 50).ErrorDetails().(map[string]any)
	if details["until"] != until.UTC().Format(time.RFC3339) || details["plan"] != "subscription" {
		t.Errorf("the refusal's details do not say which limit applies: %v", details)
	}
}

// No per-account limit — none wired, or a limit of 0 — is exactly the old
// behaviour: the gate is asked with the instance cap alone and admits.
func TestNoAccountLimitIsTheInstanceCapAlone(t *testing.T) {
	for name, resolver := range map[string]AccountDeviceLimitResolver{
		"unwired": nil, "zero": freeLimit(0),
	} {
		t.Run(name, func(t *testing.T) {
			h := newCapHarness(nil, 0, fixedCap{cap: 30})
			h.repo.accountAdmitted = 1000
			if resolver != nil {
				h.svc.SetAccountDeviceLimitResolver(resolver)
			}
			h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
			if _, err := h.svc.Create(context.Background(), node_create.NodeCreateCommand{
				ID: uuid.New(), Name: "n", AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
			}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if l := h.repo.lastLimits; l.InstanceCap != 30 || l.Account.Applies() {
				t.Errorf("gate asked with %+v, want the instance cap of 30 and no account limit", l)
			}
		})
	}
}

// An account above its limit — a subscription that ended, an override that
// was lowered — keeps every device it has: a reconnect is never an
// admission, so it never even asks the limit.
func TestAnAccountOverItsLimitKeepsItsDevices(t *testing.T) {
	accountID := uuid.New()
	existing := admittedNode(accountID)
	calls := 0
	h := accountHarness(12, fixedAccountLimit{limit: entitlement.AccountLimit{Limit: 5, Plan: entitlement.AccountLimitPlanFree}, calls: &calls})
	h.repo.existing = existing

	got, err := h.svc.ConnectWithMasterToken(context.Background(), uuid.New(), accountID,
		node.HardwareFingerprint{HardwareSerial: fullSerial, MacAddress: realMAC}, services.NodeDefaults{})
	if err != nil || got == nil || got.ID != existing.ID {
		t.Fatalf("an admitted device of an account over its limit was refused: %v", err)
	}
	if h.repo.capCalls != 0 || calls != 0 {
		t.Errorf("the reconnect asked the gate %d time(s) and the limit %d time(s)", h.repo.capCalls, calls)
	}
	_, err = h.adoptNew(t, accountID)
	wantAccountLimitRefusal(t, err, 5, 12)
}

// A limit that cannot be read never lifts it: the admission is refused as
// unverified, with nothing written.
func TestAnUnreadableAccountLimitRefusesTheAdmission(t *testing.T) {
	h := accountHarness(0, fixedAccountLimit{err: errors.New("connection reset")})
	h.svc.sshKeyRepo = sshKeyRepoWithNoDefault{}
	_, err := h.svc.Create(context.Background(), node_create.NodeCreateCommand{
		ID: uuid.New(), Name: "n", AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if !errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		t.Fatalf("err = %v, want the unverified refusal", err)
	}
	if h.repo.capCalls != 0 || len(h.repo.created) != 0 {
		t.Error("an admission whose limit could not be read reached the write")
	}
}

// The SSH adoption's pre-check refuses at the account's limit before any job.
func TestCheckDeviceCapRefusesAtTheAccountLimit(t *testing.T) {
	accountID := uuid.New()
	h := accountHarness(5, freeLimit(5))
	wantAccountLimitRefusal(t, h.svc.CheckDeviceCap(context.Background(), audit.UserActor(accountID, uuid.New())), 5, 5)
	if err := accountHarness(4, freeLimit(5)).svc.CheckDeviceCap(context.Background(), audit.UserActor(accountID, uuid.New())); err != nil {
		t.Errorf("below the account limit: %v", err)
	}
}
