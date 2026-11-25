package utility

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// --- the drift decision, which is where the rules live -------------------

func driftSet(id, sourceHash string) []utility.Resolved {
	return []utility.Resolved{{
		Definition: utility.Definition{ID: id, Name: id, Class: utility.ClassReconciled},
		ConfigHash: sourceHash,
	}}
}

// A resolved utility with no ladder row has never been applied here: a node
// that joined an OU with assignments, or a reimaged node whose ladder
// install_os dropped. That is the "future joiners included" promise.
func TestDriftingUtilitiesConvergesAnUnknownUtility(t *testing.T) {
	drifted, exhausted := driftingUtilities(driftSet("docker", "h1"), map[string]utility.NodeState{})
	if len(drifted) != 1 || drifted[0] != "docker" || len(exhausted) != 0 {
		t.Fatalf("drifted=%v exhausted=%v, want [docker] and none", drifted, exhausted)
	}
}

// A converged utility whose config has not moved is left alone — the loop
// must be silent in the steady state, which is every report of a healthy node.
func TestDriftingUtilitiesIgnoresAConvergedUtility(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "h1", DesiredHash: "r1", AppliedHash: "r1",
	}}
	drifted, _ := driftingUtilities(driftSet("docker", "h1"), states)
	if len(drifted) != 0 {
		t.Fatalf("drifted=%v, want none for a converged utility", drifted)
	}
}

// The assignment changed while the node was away: the freshly resolved source
// hash no longer matches what the last apply recorded.
func TestDriftingUtilitiesConvergesAChangedConfig(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "old", DesiredHash: "r1", AppliedHash: "r1",
	}}
	drifted, _ := driftingUtilities(driftSet("docker", "new"), states)
	if len(drifted) != 1 {
		t.Fatalf("drifted=%v, want the changed config", drifted)
	}
}

// Recorded desired but never confirmed applied — the dispatch expired while
// the node was away. This is drift, not a converged rung.
func TestDriftingUtilitiesConvergesADeclaredButUnappliedUtility(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "h1", DesiredHash: "r1", AppliedHash: "",
	}}
	drifted, _ := driftingUtilities(driftSet("docker", "h1"), states)
	if len(drifted) != 1 {
		t.Fatalf("drifted=%v, want the unconfirmed utility", drifted)
	}
}

// The 3-strike cap: the same config failing three times stands down, so a
// utility that cannot install does not re-dispatch on every reconnect
// forever.
func TestDriftingUtilitiesStandsDownAfterThreeFailures(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "h1", LastError: "failed: apt", DriftFailures: 3,
	}}
	drifted, exhausted := driftingUtilities(driftSet("docker", "h1"), states)
	if len(drifted) != 0 || len(exhausted) != 1 {
		t.Fatalf("drifted=%v exhausted=%v, want stand-down", drifted, exhausted)
	}
	// Two failures still has budget — the cap is three, not "any failure".
	states["docker"] = utility.NodeState{
		UtilityID: "docker", DesiredSourceHash: "h1", LastError: "failed: apt", DriftFailures: 2,
	}
	if drifted, _ = driftingUtilities(driftSet("docker", "h1"), states); len(drifted) != 1 {
		t.Fatalf("drifted=%v, want a retry while budget remains", drifted)
	}
}

// An EDITED config escapes the stand-down: the stored counter belongs to the
// config that was failing, and the operator's fix must be tried.
func TestDriftingUtilitiesRetriesAnEditedConfigDespiteFailures(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "old", LastError: "failed: apt", DriftFailures: 9,
	}}
	drifted, exhausted := driftingUtilities(driftSet("docker", "new"), states)
	if len(drifted) != 1 || len(exhausted) != 0 {
		t.Fatalf("drifted=%v exhausted=%v, want the edited config retried", drifted, exhausted)
	}
}

// A row written before desired_source_hash existed says nothing about drift.
// Treating an empty hash as "changed" would re-apply the whole fleet once, on
// the upgrade that introduced the loop.
func TestDriftingUtilitiesIgnoresALegacyRowWithNoSourceHash(t *testing.T) {
	states := map[string]utility.NodeState{"docker": {
		UtilityID: "docker", DesiredSourceHash: "", DesiredHash: "r1", AppliedHash: "r1",
	}}
	drifted, _ := driftingUtilities(driftSet("docker", "h1"), states)
	if len(drifted) != 0 {
		t.Fatalf("drifted=%v, want a legacy converged row left alone", drifted)
	}
}

// --- the dispatch guards -------------------------------------------------

type reconcileStates struct {
	applyFakeStates
	rows []utility.NodeState
	err  error
}

func (f reconcileStates) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]utility.NodeState, error) {
	return f.rows, f.err
}

type fakeApplyCounter struct {
	live      int64
	liveErr   error
	active    int64
	activeErr error
}

func (f *fakeApplyCounter) CountActiveByAccountAndSlug(context.Context, uuid.UUID, string) (int64, error) {
	return f.active, f.activeErr
}

func (f *fakeApplyCounter) CountLiveByNodeAndSlug(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return f.live, f.liveErr
}

func reconcileFixture(t *testing.T, rows []utility.NodeState, counter *fakeApplyCounter) (*Service, *multiDispatcher, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	catalog := &utility.Catalog{Utilities: []utility.Definition{{
		ID: "docker", Name: "Docker", Class: utility.ClassReconciled,
		Params: []workflow_block.ParamSpec{{Name: "channel", Type: workflow_block.ParamString, Default: "distro"}},
	}}}
	rowsCopy := []utility.Assignment{{
		ID: uuid.New(), AccountID: accountID, UtilityID: "docker",
		AccountWide: true, Enabled: true, Config: map[string]interface{}{},
	}}
	dispatcher := &multiDispatcher{}
	svc := &Service{
		catalog:      catalog,
		assignments:  &settleFakeAssignments{rows: rowsCopy},
		states:       reconcileStates{rows: rows},
		nodeRepo:     &canaryFakeNodeRepo{nodes: []node.Node{{ID: nodeID, Name: "n1", Status: node.StatusOnline}}},
		logger:       slog.Default(),
		dispatcher:   dispatcher,
		applyCounter: counter,
	}
	return svc, dispatcher, accountID, nodeID
}

// A node with no ladder rows converges: the fresh-joiner case.
func TestReconcileNodeDispatchesForAFreshNode(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil, &fakeApplyCounter{})
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	if got := len(dispatcher.snapshot()); got != 1 {
		t.Fatalf("dispatched %d, want 1", got)
	}
}

// An apply already registered/sent/in-progress owns this node. Re-dispatching
// would race — and supersede — the queued command delivered on this very
// reconnect.
func TestReconcileNodeSkipsWhenAnApplyIsLive(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil, &fakeApplyCounter{live: 1})
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	if got := len(dispatcher.snapshot()); got != 0 {
		t.Fatalf("dispatched %d, want none while an apply is live", got)
	}
}

// The live count failing is not a licence to dispatch: this guard fails
// CLOSED, unlike the count caps (which fail open, because refusing an
// operator's action over a read error is the worse harm).
func TestReconcileNodeFailsClosedOnAnUnreadableLiveCount(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil,
		&fakeApplyCounter{liveErr: errors.New("db down")})
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	if got := len(dispatcher.snapshot()); got != 0 {
		t.Fatalf("dispatched %d, want none when the guard cannot be read", got)
	}
}

// The steady state — a converged node reporting system info every few minutes
// — must dispatch nothing at all.
func TestReconcileNodeIsSilentWhenConverged(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil, &fakeApplyCounter{})
	// Resolve once to learn the real source hash this fixture produces, then
	// record it as converged.
	set, _, err := svc.ResolveForNode(context.Background(), accountID, nodeID)
	if err != nil || len(set) != 1 {
		t.Fatalf("fixture resolve: %v (%d)", err, len(set))
	}
	svc.states = reconcileStates{rows: []utility.NodeState{{
		UtilityID:         "docker",
		DesiredSourceHash: set[0].ConfigHash,
		DesiredHash:       "r1",
		AppliedHash:       "r1",
	}}}
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	if got := len(dispatcher.snapshot()); got != 0 {
		t.Fatalf("dispatched %d, want silence in the steady state", got)
	}
}

// The pacing bound applies to the loop too: a rack powering back on reports
// system info for every node at once.
func TestReconcileNodeRespectsThePacingBound(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil, &fakeApplyCounter{active: 5})
	svc.maxConcurrentApplies = 5
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	if got := len(dispatcher.snapshot()); got != 0 {
		t.Fatalf("dispatched %d, want none at the pacing bound", got)
	}
}

// The reconcile apply is the PLATFORM's: it carries no user, so the command's
// audit row records SystemActor instead of attributing it to whoever last
// touched the assignment.
func TestReconcileNodeDispatchesAsTheSystem(t *testing.T) {
	svc, dispatcher, accountID, nodeID := reconcileFixture(t, nil, &fakeApplyCounter{})
	svc.ReconcileNode(context.Background(), accountID, nodeID)
	cmds := dispatcher.snapshot()
	if len(cmds) != 1 {
		t.Fatalf("dispatched %d, want 1", len(cmds))
	}
	if cmds[0].AuthContext.GetUserID() != uuid.Nil {
		t.Fatalf("user = %v, want the nil (system) actor", cmds[0].AuthContext.GetUserID())
	}
	if cmds[0].Command != node_command.SlugApplyUtilityConfig {
		t.Fatalf("slug = %q", cmds[0].Command)
	}
}
