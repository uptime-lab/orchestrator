package node_command

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

type awaitingStubNodeService struct {
	services.NodeService
	marked []string
}

func (s *awaitingStubNodeService) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	return nil, nil
}

// An install_os among the swept commands is sent back to netboot; that rule
// is tested on its own (install_failure_sweep_test.go).
func (s *awaitingStubNodeService) ForceNetbootAfterFailedInstall(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *awaitingStubNodeService) MarkAwaitingReturn(_ context.Context, _ uuid.UUID, slug string) error {
	s.marked = append(s.marked, slug)
	return nil
}

// The stamp is what turns a bare "offline" into "never came back after
// <slug>" in the UI. Two rules: the reboot wins when present — that command's
// whole meaning was "come back" — and otherwise the LAST settled command
// names what the node was doing.
func TestUnreachableSweepStampsTheRebootFirst(t *testing.T) {
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{
		cmd(node_command.SlugInstallOS, node_command.StatusInProgress),
		cmd(node_command.SlugReboot, node_command.StatusSent),
	}}
	svc, _, _ := unreachableService(repo)
	ns := &awaitingStubNodeService{}
	svc.SetNodeService(ns)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.marked) != 1 || ns.marked[0] != node_command.SlugReboot {
		t.Fatalf("stamped %v, want exactly [reboot]", ns.marked)
	}
}

func TestUnreachableSweepStampsTheLastCommandWithoutAReboot(t *testing.T) {
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{
		cmd(node_command.SlugSetHostname, node_command.StatusInProgress),
		cmd(node_command.SlugInstallOS, node_command.StatusInProgress),
	}}
	svc, _, _ := unreachableService(repo)
	ns := &awaitingStubNodeService{}
	svc.SetNodeService(ns)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.marked) != 1 || ns.marked[0] != node_command.SlugInstallOS {
		t.Fatalf("stamped %v, want [install_os]", ns.marked)
	}
}

// Nothing settled → nothing to expect back → no stamp: a node that died IDLE
// is honestly just offline.
func TestQuietUnreachableNodeGetsNoStamp(t *testing.T) {
	repo := &stubCommandRepo{}
	svc, _, _ := unreachableService(repo)
	ns := &awaitingStubNodeService{}
	svc.SetNodeService(ns)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(ns.marked) != 0 {
		t.Fatalf("stamped %v, want nothing", ns.marked)
	}
}
