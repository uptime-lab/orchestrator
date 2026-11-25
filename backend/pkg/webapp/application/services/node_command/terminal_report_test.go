package node_command

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// A command's stored result is evidence — the output of a run_script or an
// os-users scan an operator has already read. Command ownership is checked
// against the account and the node, and terminal commands stay readable for the
// whole retention window, so a node can report on its own long-finished
// commands. Nothing about such a report may reach the database.

// terminalReportRepo answers only what this path calls; the embedded interface
// is nil, so anything else panics loudly.
type terminalReportRepo struct {
	repository.NodeCommandRepository

	current      node_command.NodeCommand
	setResults   int
	statusWrites int
}

func (r *terminalReportRepo) GetByIDForUpdate(context.Context, uuid.UUID) (*node_command.NodeCommand, error) {
	copied := r.current
	return &copied, nil
}

func (r *terminalReportRepo) SetResult(context.Context, uuid.UUID, map[string]string) error {
	r.setResults++
	return nil
}

func (r *terminalReportRepo) UpdateStatus(context.Context, uuid.UUID, node_command.NodeCommandStatus, int, string) error {
	r.statusWrites++
	return nil
}

func (r *terminalReportRepo) GetWorkflowExecutionNodeID(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}

func terminalReportService(repo *terminalReportRepo) *Service {
	recorder, _ := audittest.New()
	return NewService(repo, &stubWebsocket{}, recorder, slog.Default())
}

func TestAReportForAFinishedCommandRewritesNothingAndSaysItDidNotApply(t *testing.T) {
	for _, status := range []node_command.NodeCommandStatus{
		node_command.StatusSuccess,
		node_command.StatusFailed,
		node_command.StatusSuperseded,
		node_command.StatusExpired,
		node_command.StatusSkipped,
	} {
		t.Run(string(status), func(t *testing.T) {
			repo := &terminalReportRepo{current: node_command.NodeCommand{
				ID:     uuid.New(),
				NodeID: uuid.New(),
				Slug:   node_command.SlugRunScript,
				Status: status,
			}}
			svc := terminalReportService(repo)

			applied, err := svc.UpdateNodeCommandStatusWithResult(context.Background(), repo.current.ID,
				"success", 100, "replayed", map[string]string{"stdout": "attacker-chosen"})
			if err != nil {
				t.Fatalf("a replayed report must be ignored, not fail: %v", err)
			}
			if applied {
				t.Error("the report was reported as applied; the caller will settle a transition that never happened")
			}
			if repo.setResults != 0 {
				t.Errorf("the stored result was rewritten %d time(s) by a report for an already-finished command", repo.setResults)
			}
			if repo.statusWrites != 0 {
				t.Errorf("the status was written %d time(s) for an already-finished command", repo.statusWrites)
			}
		})
	}
}

// The ordinary path must be untouched: a live command still records its outputs
// before its status, so a workflow step reading them on completion sees them.
func TestALiveCommandStillRecordsItsResult(t *testing.T) {
	repo := &terminalReportRepo{current: node_command.NodeCommand{
		ID:     uuid.New(),
		NodeID: uuid.New(),
		Slug:   node_command.SlugRunScript,
		Status: node_command.StatusSent,
	}}
	svc := terminalReportService(repo)

	applied, err := svc.UpdateNodeCommandStatusWithResult(context.Background(), repo.current.ID,
		"success", 100, "done", map[string]string{"stdout": "hello"})
	if err != nil {
		t.Fatalf("reporting: %v", err)
	}
	if !applied {
		t.Error("a real report on a live command must report as applied")
	}
	if repo.setResults != 1 || repo.statusWrites != 1 {
		t.Errorf("result writes %d, status writes %d; want 1 and 1", repo.setResults, repo.statusWrites)
	}
}
