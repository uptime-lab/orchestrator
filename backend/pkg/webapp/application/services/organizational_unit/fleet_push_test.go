package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// An OU-level or account-level ssh_password_auth change altered a
// node's RESOLVED access state and dispatched nothing, so the revocation landed
// only in the database: `GET /nodes/:id/effective-access` reported the new state
// immediately and the device kept accepting the shared password. The only healer
// is reconcileAccessGeneration, reachable solely from UpdateSystemInfo, which the
// agent sends on connect and not on a timer — so on a node that stays up it never
// landed at all.
//
// The interesting half is not "does it dispatch" but WHICH nodes: the flag is
// tri-state and inherited node -> OU -> root -> account, so the affected set is
// not "the nodes whose ou_id is X". An edit to a root reaches nodes that are not
// in it (through child units on inherit) and misses nodes that are (those with
// their own value).

type fleetNodeRepo struct {
	repository.NodeRepository
	nodes []node.Node
}

func (f *fleetNodeRepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]node.Node, error) {
	return f.nodes, nil
}

type fleetOURepo struct {
	repository.OrganizationalUnitRepository
	ous     []organizational_unit.OrganizationalUnit
	updated bool
}

func (f *fleetOURepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return f.ous, nil
}

func (f *fleetOURepo) GetByID(_ context.Context, _, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	for i := range f.ous {
		if f.ous[i].ID == id {
			copied := f.ous[i]
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fleetOURepo) Update(_ context.Context, _ *organizational_unit.OrganizationalUnit) error {
	f.updated = true
	return nil
}

// Real, not a stub returning zero: applyReparent refuses to nest a unit that has
// children, and a fake that always says "none" would let a test drive a move the
// production code rejects.
func (f *fleetOURepo) CountChildren(_ context.Context, _, ouID uuid.UUID) (int64, error) {
	var n int64
	for i := range f.ous {
		if f.ous[i].ParentID != nil && *f.ous[i].ParentID == ouID {
			n++
		}
	}
	return n, nil
}

type fleetAccountPolicyRepo struct {
	repository.AccountPolicyRepository
	policy *organizational_unit.AccountPolicy
}

func (f *fleetAccountPolicyRepo) Get(_ context.Context, _ uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	return f.policy, nil
}

func (f *fleetAccountPolicyRepo) Upsert(_ context.Context, p *organizational_unit.AccountPolicy) error {
	f.policy = p
	return nil
}

// concurrentApplier is the recording applier made safe for the detached
// dispatch, which runs on its own goroutine.
type concurrentApplier struct {
	mu    sync.Mutex
	nodes []uuid.UUID
}

func (a *concurrentApplier) EnqueueApplyAccessConfig(_ context.Context, _, nodeID uuid.UUID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nodes = append(a.nodes, nodeID)
	return nil
}

func (a *concurrentApplier) seen() []uuid.UUID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]uuid.UUID(nil), a.nodes...)
}

// waitFor polls until the detached goroutine has dispatched `want` nodes, or
// gives up. Polling rather than sleeping a fixed time: the assertion is about
// what was dispatched, and a fixed sleep either flakes or wastes a second.
func (a *concurrentApplier) waitFor(t *testing.T, want int) []uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := a.seen()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatched %d applies, want %d", len(got), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func boolPtr(b bool) *bool { return &b }

// fleetFixture builds one account: a root unit with a child unit, and five nodes
// covering every way the flag can resolve.
type fleetFixture struct {
	svc       *Service
	applier   *concurrentApplier
	ouRepo    *fleetOURepo
	accountID uuid.UUID

	rootID, childID uuid.UUID
	// inRootInherit resolves at the ROOT; inChildInherit is in the CHILD but the
	// child is on inherit, so it ALSO resolves at the root — the case a
	// "nodes whose ou_id is X" query gets wrong.
	inRootInherit, inChildInherit uuid.UUID
	// inRootOwnValue has its own flag, so no level above it reaches it.
	inRootOwnValue uuid.UUID
	// inChildSet is in a child that has its own value, so the root does not
	// reach it either.
	inChildSet uuid.UUID
	// unassigned belongs to no unit and can only be reached by the account.
	unassigned uuid.UUID
}

func newFleetFixture(rootFlag, childFlag *bool) *fleetFixture {
	f := &fleetFixture{
		applier:        &concurrentApplier{},
		accountID:      uuid.New(),
		rootID:         uuid.New(),
		childID:        uuid.New(),
		inRootInherit:  uuid.New(),
		inChildInherit: uuid.New(),
		inRootOwnValue: uuid.New(),
		inChildSet:     uuid.New(),
		unassigned:     uuid.New(),
	}
	// A second child, carrying its own value, holds inChildSet.
	childSetID := uuid.New()
	f.ouRepo = &fleetOURepo{ous: []organizational_unit.OrganizationalUnit{
		{ID: f.rootID, SSHPasswordAuth: rootFlag},
		{ID: f.childID, ParentID: &f.rootID, SSHPasswordAuth: childFlag},
		{ID: childSetID, ParentID: &f.rootID, SSHPasswordAuth: boolPtr(true)},
	}}
	nodes := []node.Node{
		{ID: f.inRootInherit, OUID: &f.rootID},
		{ID: f.inChildInherit, OUID: &f.childID},
		{ID: f.inRootOwnValue, OUID: &f.rootID, SSHPasswordAuth: boolPtr(false)},
		{ID: f.inChildSet, OUID: &childSetID},
		{ID: f.unassigned},
	}
	recorder, _ := testAuditRecorder()
	f.svc = &Service{
		ouRepo:            f.ouRepo,
		nodeRepo:          &fleetNodeRepo{nodes: nodes},
		accountPolicyRepo: &fleetAccountPolicyRepo{policy: &organizational_unit.AccountPolicy{AccountID: f.accountID}},
		accessApplier:     f.applier,
		audit:             recorder,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

func contains(ids []uuid.UUID, id uuid.UUID) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func TestRootLevelChangeReachesInheritingChildNodesOnly(t *testing.T) {
	// The root carries the value; the child is on inherit.
	f := newFleetFixture(boolPtr(true), nil)

	got := f.svc.nodesResolvingPasswordAuthAt(context.Background(), f.accountID, &f.rootID)

	if !contains(got, f.inRootInherit) {
		t.Error("a node directly in the edited unit was not included")
	}
	if !contains(got, f.inChildInherit) {
		t.Error("a node in a CHILD unit on inherit follows the root and must be included")
	}
	if contains(got, f.inRootOwnValue) {
		t.Error("a node with its own flag ignores every level above it and must not be pushed")
	}
	if contains(got, f.inChildSet) {
		t.Error("a node whose child unit sets the flag does not follow the root")
	}
	if contains(got, f.unassigned) {
		t.Error("an unassigned node is not reachable from any unit")
	}
	if len(got) != 2 {
		t.Errorf("dispatched to %d nodes, want exactly 2: %v", len(got), got)
	}
}

func TestAccountLevelChangeReachesOnlyNodesNobodyElseAnswers(t *testing.T) {
	// Nothing in the tree has an opinion, so the account is the decider for
	// everything except the node carrying its own value.
	f := newFleetFixture(nil, nil)

	got := f.svc.nodesResolvingPasswordAuthAt(context.Background(), f.accountID, nil)

	if !contains(got, f.unassigned) {
		t.Error("an unassigned node is reachable ONLY from the account and must be included")
	}
	if !contains(got, f.inRootInherit) || !contains(got, f.inChildInherit) {
		t.Error("nodes inheriting all the way up follow the account")
	}
	if contains(got, f.inRootOwnValue) {
		t.Error("a node with its own flag must not be pushed")
	}
	if contains(got, f.inChildSet) {
		t.Error("a node whose unit sets the flag does not follow the account")
	}
	if len(got) != 3 {
		t.Errorf("dispatched to %d nodes, want exactly 3: %v", len(got), got)
	}
}

func TestUpdatingAUnitPushesThePasswordAuthChange(t *testing.T) {
	f := newFleetFixture(nil, nil)

	off := organizational_unit.PolicyOff
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:              f.rootID,
		SSHPasswordAuth: &off,
		AuthContext:     commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Two, not three: the unassigned node is reachable only from the account.
	got := f.applier.waitFor(t, 2)
	if !contains(got, f.inRootInherit) || !contains(got, f.inChildInherit) {
		t.Errorf("the inheriting nodes were not pushed: %v", got)
	}
	if contains(got, f.unassigned) {
		t.Errorf("an OU edit reached an unassigned node: %v", got)
	}
}

// Setting a unit back to inherit is the case that shaped the resolver: the
// effective value of its nodes changes (they start following the account) at
// exactly the moment the unit stops being the one that answers. A resolver keyed
// on "which level currently decides" pushes nothing here, which is why this asks
// "does the chain reach this level" instead.
func TestSettingAUnitBackToInheritStillPushes(t *testing.T) {
	f := newFleetFixture(boolPtr(false), nil)

	inherit := organizational_unit.PolicyInherit
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:              f.rootID,
		SSHPasswordAuth: &inherit,
		AuthContext:     commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := f.applier.waitFor(t, 2)
	if !contains(got, f.inRootInherit) || !contains(got, f.inChildInherit) {
		t.Errorf("nodes that stopped following this unit were not pushed: %v", got)
	}
}

// A full-document PUT re-sends every field it holds, so a rename arrives with
// ssh_password_auth present and unchanged. Pushing on "the field was supplied"
// rather than "the value changed" would rewrite authorized_keys.d across the
// fleet every time somebody renames a unit.
func TestRenamingAUnitPushesNothing(t *testing.T) {
	f := newFleetFixture(boolPtr(false), nil)

	name := "Production"
	off := organizational_unit.PolicyOff // the value it already has
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:              f.rootID,
		Name:            &name,
		SSHPasswordAuth: &off,
		AuthContext:     commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Give a dispatch that should not happen a chance to happen anyway.
	time.Sleep(50 * time.Millisecond)
	if got := f.applier.seen(); len(got) != 0 {
		t.Errorf("an unchanged flag pushed %d applies: %v", len(got), got)
	}
}

func TestAccountPolicyChangePushesToTheFleet(t *testing.T) {
	f := newFleetFixture(nil, nil)

	off := organizational_unit.PolicyOff
	if _, err := f.svc.SetAccountPolicy(context.Background(), account_policy_update.AccountPolicyUpdateCommand{
		SSHPasswordAuth: &off,
		AuthContext:     commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("SetAccountPolicy: %v", err)
	}

	got := f.applier.waitFor(t, 3)
	if !contains(got, f.unassigned) {
		t.Errorf("the unassigned node — reachable only from the account — was not pushed: %v", got)
	}
}

// Moving a unit changes which ROOT its nodes inherit from — the same event as
// moving a node between units, which `AssignNode` already pushes for. The drag
// in the OU rail sends `{parent_id}` and nothing else, so a gate keyed only on
// the password-auth value never fires: the unit's nodes keep the OLD root's
// access entries (keys, sudo, password) on the device while the API reports the
// new ones. `applyReparent` requires the unit to be a LEAF but never requires it
// to be EMPTY, so this moves a whole unit's worth of nodes in one gesture.
func TestReparentingAUnitPushesItsNodes(t *testing.T) {
	f := newFleetFixture(nil, nil)

	// A second root to move the child unit under.
	otherRootID := uuid.New()
	f.ouRepo.ous = append(f.ouRepo.ous, organizational_unit.OrganizationalUnit{ID: otherRootID})

	parent := otherRootID.String()
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:          f.childID,
		ParentID:    &parent,
		AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := f.applier.waitFor(t, 1)
	if !contains(got, f.inChildInherit) {
		t.Errorf("the moved unit's node was not pushed: %v", got)
	}
	if contains(got, f.inRootInherit) || contains(got, f.unassigned) {
		t.Errorf("a reparent pushed nodes outside the moved unit: %v", got)
	}
}

// Un-parenting is the same move in the other direction: the unit becomes a root
// and its nodes stop inheriting the old root's entries entirely.
func TestUnParentingAUnitAlsoPushes(t *testing.T) {
	f := newFleetFixture(nil, nil)

	root := ""
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:          f.childID,
		ParentID:    &root,
		AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got := f.applier.waitFor(t, 1); !contains(got, f.inChildInherit) {
		t.Errorf("un-parenting pushed nothing for the unit's node: %v", got)
	}
}

// A rename carries no structural change, so it must still push nothing — the
// same property the password-auth gate has.
func TestRenamingAUnitDoesNotLookLikeAReparent(t *testing.T) {
	f := newFleetFixture(nil, nil)

	name := "Renamed"
	if _, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:          f.childID,
		Name:        &name,
		AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if got := f.applier.seen(); len(got) != 0 {
		t.Errorf("a rename pushed %d applies: %v", len(got), got)
	}
}
