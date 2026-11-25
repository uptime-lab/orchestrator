package organizational_unit

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

type recordingNodeNotifier struct{ nodes []uuid.UUID }

func (r *recordingNodeNotifier) NotifyNodeChanged(_ context.Context, _, nodeID uuid.UUID) {
	r.nodes = append(r.nodes, nodeID)
}

// A policy override set from the Organization page is a column of the node;
// open node lists are told, or they keep showing the old value (and a control
// lit on the old value ignores a click on it).
func TestSetNodePolicyPushesTheNodeChange(t *testing.T) {
	repo := &policyFakeNodeRepo{n: &node.Node{ID: uuid.New()}}
	notifier := &recordingNodeNotifier{}
	s := &Service{nodeRepo: repo, audit: testRecorder()}
	s.SetNodeChangeNotifier(notifier)

	if err := s.SetNodePolicy(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), repo.n.ID,
		nil, flag(organizational_unit.PolicyOn), nil); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}
	if len(notifier.nodes) != 1 || notifier.nodes[0] != repo.n.ID {
		t.Fatalf("notified %v, want the one node", notifier.nodes)
	}
}
