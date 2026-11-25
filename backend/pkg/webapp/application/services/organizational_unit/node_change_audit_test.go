package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

func testRecorder() *audit.Recorder {
	r, _ := audittest.New()
	return r
}

// Setting a per-node policy flag can open password SSH on the device or change
// how it boots, so it is recorded under the member who did it, naming the
// flags and their new values.
func TestSetNodePolicyIsAuditedUnderTheActor(t *testing.T) {
	recorder, sink := audittest.New()
	accountID, userID := uuid.New(), uuid.New()
	repo := &policyFakeNodeRepo{n: &node.Node{ID: uuid.New(), Name: "kiosk-1"}}
	s := &Service{nodeRepo: repo, audit: recorder, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := s.SetNodePolicy(context.Background(), audit.UserActor(accountID, userID), accountID, repo.n.ID,
		flag(organizational_unit.PolicyOn), nil, flag(organizational_unit.PolicyInherit)); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionNodePolicyUpdate)
	if !ok {
		t.Fatalf("no %s row; actions = %v", audit_event.ActionNodePolicyUpdate, sink.Actions())
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Fatalf("actor = %v, want the member %s", event.ActorUserID, userID)
	}
	if event.TargetType != audit_event.TargetNode || event.TargetID != repo.n.ID.String() {
		t.Fatalf("target = %s/%s, want node/%s", event.TargetType, event.TargetID, repo.n.ID)
	}
	for _, want := range []string{"network_first=on", "ssh_password_auth=inherit"} {
		if !strings.Contains(event.Detail, want) {
			t.Fatalf("detail %q does not name %q", event.Detail, want)
		}
	}
	if strings.Contains(event.Detail, "boot_to_os") {
		t.Fatalf("detail %q names a flag the call did not set", event.Detail)
	}
}

// Moving a node between units swaps every unit-level access entry it carries;
// the move is recorded under the member with the previous and the new unit.
func TestAssignNodeIsAuditedUnderTheActor(t *testing.T) {
	recorder, sink := audittest.New()
	accountID, userID := uuid.New(), uuid.New()
	from, to := uuid.New(), uuid.New()
	nodeID := uuid.New()
	s := &Service{
		ouMutationRepo: &assignFakeMutationRepo{},
		nodeRepo:       &policyFakeNodeRepo{n: &node.Node{ID: nodeID, OUID: &from}},
		audit:          recorder,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := s.AssignNode(context.Background(), audit.UserActor(accountID, userID), accountID, nodeID, &to); err != nil {
		t.Fatalf("AssignNode: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionNodeAssignOU)
	if !ok {
		t.Fatalf("no %s row; actions = %v", audit_event.ActionNodeAssignOU, sink.Actions())
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Fatalf("actor = %v, want the member %s", event.ActorUserID, userID)
	}
	if event.TargetID != nodeID.String() {
		t.Fatalf("target = %s, want the node %s", event.TargetID, nodeID)
	}
	if want := "from=" + from.String() + " to=" + to.String(); event.Detail != want {
		t.Fatalf("detail = %q, want %q", event.Detail, want)
	}
}

// A refused move records nothing: the trail must not claim a change that did
// not happen.
func TestRefusedAssignNodeIsNotAudited(t *testing.T) {
	recorder, sink := audittest.New()
	to := uuid.New()
	s := &Service{
		ouMutationRepo: &assignFakeMutationRepo{err: context.Canceled},
		nodeRepo:       &policyFakeNodeRepo{n: &node.Node{ID: uuid.New()}},
		audit:          recorder,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := s.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), uuid.New(), &to); err == nil {
		t.Fatal("the refusal must surface")
	}
	if got := sink.Actions(); len(got) != 0 {
		t.Fatalf("a refused move recorded %v", got)
	}
}
