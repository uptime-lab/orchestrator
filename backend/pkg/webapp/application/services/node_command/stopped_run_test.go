package node_command

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// statusRecordingNodeService records the node-status writes a settle makes, on
// top of the forced netboots bootRuleNodeService records.
type statusRecordingNodeService struct {
	bootRuleNodeService
	statuses []string
}

func (s *statusRecordingNodeService) UpdateStatusWithAccount(_ context.Context, _, _ uuid.UUID, status string) error {
	s.statuses = append(s.statuses, status)
	return nil
}

func stoppedRunService(c node_command.NodeCommand) (*Service, *liveCommandRepo, *statusRecordingNodeService, *stubWorkflowService) {
	repo := newLiveCommandRepo("", c)
	svc, workflow, _ := unreachableService(&stubCommandRepo{})
	svc.repo = repo
	ns := &statusRecordingNodeService{}
	svc.SetNodeService(ns)
	return svc, repo, ns, workflow
}

// A stopped run settles its block's command whatever stage it had reached. The
// agent acknowledges with in_progress at once, so the case that matters most is
// the one the send-path settler skipped: the command stayed in_progress and the
// node in maintenance until the device happened to report.
func TestAStoppedRunFailsItsCommandAtEveryStage(t *testing.T) {
	for _, status := range []node_command.NodeCommandStatus{
		node_command.StatusRegistered, node_command.StatusSent, node_command.StatusInProgress,
	} {
		t.Run(string(status), func(t *testing.T) {
			c := cmd(node_command.SlugRunScript, status)
			svc, repo, ns, workflow := stoppedRunService(c)

			if err := svc.FailCommandOfStoppedRun(context.Background(), c.ID, "the run was cancelled"); err != nil {
				t.Fatalf("settling: %v", err)
			}
			if got := repo.cmds[c.ID].Status; got != node_command.StatusFailed {
				t.Fatalf("command status = %q, want failed", got)
			}
			if len(ns.statuses) != 1 || ns.statuses[0] != string(node.StatusOnline) {
				t.Errorf("node status writes = %v, want the node released to online once", ns.statuses)
			}
			// The block is failed by the run's own settler; the command's
			// completion path must not drive it a second time.
			if len(workflow.completions) != 0 {
				t.Errorf("workflow completion fired %d times, want 0", len(workflow.completions))
			}
		})
	}
}

// What the device reports after the cancel changes nothing: the command is
// terminal, so the run's recorded outcome stands and no side effect re-runs.
func TestALateReportAfterAStoppedRunIsIgnored(t *testing.T) {
	c := cmd(node_command.SlugRunScript, node_command.StatusInProgress)
	svc, repo, _, workflow := stoppedRunService(c)

	if err := svc.FailCommandOfStoppedRun(context.Background(), c.ID, "the run was cancelled"); err != nil {
		t.Fatalf("settling: %v", err)
	}
	applied, err := svc.UpdateNodeCommandStatusWithResult(context.Background(), c.ID, "success", 100, "done", nil)
	if err != nil {
		t.Fatalf("late report: %v", err)
	}
	if applied {
		t.Error("a late success was applied to a command the cancel had settled")
	}
	if got := repo.cmds[c.ID].Status; got != node_command.StatusFailed {
		t.Errorf("command status = %q after the late report, want failed", got)
	}
	if len(workflow.completions) != 0 {
		t.Errorf("workflow completion fired %d times, want 0", len(workflow.completions))
	}
}

// An install_os stopped mid-write may leave a partial image, so the node goes
// back to netboot exactly as on every other route that fails an install. One
// that never left the platform touched no disk and keeps its boot target.
func TestAStoppedInstallForcesNetbootOnceItReachedTheDevice(t *testing.T) {
	for _, tc := range []struct {
		status node_command.NodeCommandStatus
		forced int
	}{
		{node_command.StatusRegistered, 0},
		{node_command.StatusSent, 1},
		{node_command.StatusInProgress, 1},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			install := cmd(node_command.SlugInstallOS, tc.status)
			svc, _, ns, _ := stoppedRunService(install)

			if err := svc.FailCommandOfStoppedRun(context.Background(), install.ID, "the run was cancelled"); err != nil {
				t.Fatalf("settling: %v", err)
			}
			if len(ns.forced) != tc.forced {
				t.Errorf("forced netboot %d times, want %d", len(ns.forced), tc.forced)
			}
		})
	}
}

// A reboot that reached the device may be taking it down right now; "online"
// would be a claim about a machine that may be mid-reboot, so its pin stays for
// the reconnect or the unreachable verdict to settle.
func TestAStoppedRebootKeepsItsPin(t *testing.T) {
	reboot := cmd(node_command.SlugReboot, node_command.StatusInProgress)
	svc, repo, ns, _ := stoppedRunService(reboot)

	if err := svc.FailCommandOfStoppedRun(context.Background(), reboot.ID, "the run was cancelled"); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if got := repo.cmds[reboot.ID].Status; got != node_command.StatusFailed {
		t.Fatalf("command status = %q, want failed", got)
	}
	if len(ns.statuses) != 0 {
		t.Errorf("node status writes = %v, want none for a reboot in flight", ns.statuses)
	}
}

// A command that finished before the run was stopped keeps its own outcome.
func TestAStoppedRunLeavesAFinishedCommandAlone(t *testing.T) {
	done := cmd(node_command.SlugRunScript, node_command.StatusSuccess)
	svc, repo, ns, _ := stoppedRunService(done)

	if err := svc.FailCommandOfStoppedRun(context.Background(), done.ID, "the run was cancelled"); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if got := repo.cmds[done.ID].Status; got != node_command.StatusSuccess {
		t.Errorf("command status = %q, want success kept", got)
	}
	if len(ns.statuses) != 0 {
		t.Errorf("node status writes = %v, want none", ns.statuses)
	}
}
