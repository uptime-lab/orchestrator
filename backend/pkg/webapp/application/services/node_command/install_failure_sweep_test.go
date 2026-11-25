package node_command

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// sweepNodeService records which nodes a sweep sent back to netboot. Any other
// node-service call panics through the embedded nil interface.
type sweepNodeService struct {
	services.NodeService
	forced []uuid.UUID
}

func (s *sweepNodeService) ForceNetbootAfterFailedInstall(_ context.Context, _, nodeID uuid.UUID) error {
	s.forced = append(s.forced, nodeID)
	return nil
}

// ForceNetbootAfterDiskErase records into the same list: both keep the node
// on recovery.
func (s *sweepNodeService) ForceNetbootAfterDiskErase(_ context.Context, _, nodeID uuid.UUID) error {
	s.forced = append(s.forced, nodeID)
	return nil
}

func (s *sweepNodeService) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	return nil, nil
}

func (s *sweepNodeService) MarkAwaitingReturn(context.Context, uuid.UUID, string) error { return nil }

func (s *sweepNodeService) UpdateStatusWithAccount(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

// staleSweepRepo adds the reconnect sweep's queries to the unreachable stub.
type staleSweepRepo struct {
	*stubCommandRepo
}

func (r staleSweepRepo) GetStalePendingNonRebootCommandsByNodeID(context.Context, uuid.UUID, node.AgentInstanceContinuity) ([]node_command.NodeCommand, error) {
	return r.pending, nil
}

func (r staleSweepRepo) HasActiveCommands(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

// An install_os a sweep settles as failed — the node stopped answering, or the
// agent came back as a different process — may have left a partial image on
// the disk. Like an install the agent reported failed, the node must be pointed
// at netboot, or its next boot runs that partial disk and goes dark.
func TestUnreachableSweepSendsAnUnfinishedInstallBackToNetboot(t *testing.T) {
	install := cmd(node_command.SlugInstallOS, node_command.StatusInProgress)
	other := cmd(node_command.SlugSetHostname, node_command.StatusInProgress)
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{install, other}}
	svc, _, _ := unreachableService(repo)
	ns := &sweepNodeService{}
	svc.SetNodeService(ns)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), install.NodeID); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.forced) != 1 || ns.forced[0] != install.NodeID {
		t.Fatalf("forced netboot for %v, want exactly the install's node %s", ns.forced, install.NodeID)
	}
}

func TestReconnectSweepSendsAnAbandonedInstallBackToNetboot(t *testing.T) {
	install := cmd(node_command.SlugInstallOS, node_command.StatusInProgress)
	repo := staleSweepRepo{&stubCommandRepo{pending: []node_command.NodeCommand{install}}}
	svc, _, _ := unreachableService(repo.stubCommandRepo)
	svc.repo = repo
	ns := &sweepNodeService{}
	svc.SetNodeService(ns)

	if err := svc.FailStalePendingCommandsForNode(context.Background(), install.NodeID, node.AgentInstanceChanged); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.forced) != 1 || ns.forced[0] != install.NodeID {
		t.Fatalf("forced netboot for %v, want exactly the install's node %s", ns.forced, install.NodeID)
	}
}

func TestSweepLeavesTheBootTargetAloneWithoutAnInstall(t *testing.T) {
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{
		cmd(node_command.SlugReboot, node_command.StatusSent),
	}}
	svc, _, _ := unreachableService(repo)
	ns := &sweepNodeService{}
	svc.SetNodeService(ns)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.forced) != 0 {
		t.Fatalf("forced netboot %d times for a node with no install, want 0", len(ns.forced))
	}
}
