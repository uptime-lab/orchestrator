package node_command

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// deletedNodeRepo serves one node's outstanding commands.
type deletedNodeRepo struct {
	stubCommandRepo
}

func (r *deletedNodeRepo) GetUnsettledCommandsByNodeID(context.Context, uuid.UUID) ([]node_command.NodeCommand, error) {
	return r.pending, nil
}

// A deleted node's queue must not outlive it: a queued command left behind is
// delivered if the node is restored under its original id, and an in_progress
// one reads "in progress" for as long as the row exists. The release_identity
// the delete itself dispatched is the one command meant to outlive the row.
func TestDeletingANodeSettlesItsOutstandingCommands(t *testing.T) {
	queued := cmd(node_command.SlugSetHostname, node_command.StatusRegistered)
	queued.Queueable = true
	running := cmd(node_command.SlugInstallOS, node_command.StatusInProgress)
	release := cmd(node_command.SlugReleaseIdentity, node_command.StatusSent)

	repo := &deletedNodeRepo{stubCommandRepo{pending: []node_command.NodeCommand{queued, running, release}}}
	svc, workflow, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	settled, err := svc.SettleCommandsForDeletedNode(context.Background(), uuid.New(), node_command.SlugReleaseIdentity)
	if err != nil {
		t.Fatalf("settling: %v", err)
	}
	if settled != 2 {
		t.Fatalf("settled = %d, want 2", settled)
	}
	if got := repo.statuses[queued.ID]; got != string(node_command.StatusExpired) {
		t.Errorf("queued command status = %q, want expired — it never left the platform", got)
	}
	if got := repo.statuses[running.ID]; got != string(node_command.StatusFailed) {
		t.Errorf("running command status = %q, want failed", got)
	}
	if got := repo.details[running.ID]; got != reasonNodeDeleted {
		t.Errorf("details = %q, want the node-deleted reason", got)
	}
	if _, touched := repo.statuses[release.ID]; touched {
		t.Error("the delete's own release_identity was settled; it must stay live")
	}
	if len(workflow.completions) != 2 {
		t.Errorf("workflow completions = %v, want both settled commands", workflow.completions)
	}
}
