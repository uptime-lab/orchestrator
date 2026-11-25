package node_command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// sweepRepo records what the backstop asked for and what it settled. It embeds
// the port through stubCommandRepo, so a method this path should not touch
// panics rather than quietly answering zero.
type sweepRepo struct {
	stubCommandRepo

	abandoned  []node_command.NodeCommand
	listErr    error
	askedAfter time.Time
	askedLimit int

	offline           []node_command.NodeCommand
	offlineErr        error
	offlineAskedAfter time.Time
}

func (s *sweepRepo) GetAbandonedInProgressCommands(_ context.Context, staleBefore time.Time, limit int) ([]node_command.NodeCommand, error) {
	s.askedAfter = staleBefore
	s.askedLimit = limit
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.pending = s.abandoned // so GetByIDForUpdate can find them
	return s.abandoned, nil
}

func (s *sweepRepo) GetUnsettledCommandsOfOfflineNodes(_ context.Context, staleBefore time.Time, _ int) ([]node_command.NodeCommand, error) {
	s.offlineAskedAfter = staleBefore
	if s.offlineErr != nil {
		return nil, s.offlineErr
	}
	s.pending = append(s.pending, s.offline...)
	return s.offline, nil
}

// The case the offline arm exists for: the monitor moved a node from
// 'maintenance' straight to 'offline' while its install_os was in_progress.
// The unreachable sweep only visits nodes still in a transient status and the
// connected-node backstop skips disk operations, so the install read
// "in progress" for as long as the device stayed away.
func TestTheBackstopSettlesAnInstallOnANodeThatWentOffline(t *testing.T) {
	install := cmd(node_command.SlugInstallOS, node_command.StatusInProgress)
	repo := &sweepRepo{offline: []node_command.NodeCommand{install}}
	svc, workflow, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo
	// The stub panics on any status write, which is the assertion that an
	// offline node is not reported back online by the settle.
	ns := &awaitingStubNodeService{}
	svc.SetNodeService(ns)

	settled, err := svc.FailAbandonedInProgressCommands(context.Background())
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}
	if got := repo.statuses[install.ID]; got != string(node_command.StatusFailed) {
		t.Errorf("status = %q, want failed", got)
	}
	if got := repo.details[install.ID]; got != reasonNodeStoppedAnswering {
		t.Errorf("details = %q, want the stopped-answering reason", got)
	}
	if len(workflow.completions) != 1 || workflow.completions[0] != install.ID {
		t.Errorf("workflow completions = %v, want the settled install", workflow.completions)
	}
	if len(ns.marked) != 1 || ns.marked[0] != node_command.SlugInstallOS {
		t.Errorf("awaiting-return stamp = %v, want [install_os]", ns.marked)
	}
}

// A reboot that never came back says so, as it does on the unreachable route.
func TestTheOfflineArmNamesARebootThatNeverReturned(t *testing.T) {
	reboot := cmd(node_command.SlugReboot, node_command.StatusSent)
	repo := &sweepRepo{offline: []node_command.NodeCommand{reboot}}
	svc, _, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	if _, err := svc.FailAbandonedInProgressCommands(context.Background()); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if got := repo.details[reboot.ID]; got != reasonRebootNeverReturned {
		t.Errorf("details = %q, want the reboot-never-returned reason", got)
	}
}

// The grace on the offline arm is what keeps a device whose link dropped
// while it kept working from losing its late report.
func TestTheOfflineArmAsksForCommandsSilentLongerThanItsGrace(t *testing.T) {
	repo := &sweepRepo{}
	svc, _, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	before := time.Now()
	if _, err := svc.FailAbandonedInProgressCommands(context.Background()); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	cutoff := before.Add(-offlineSilenceGrace)
	if repo.offlineAskedAfter.Before(cutoff.Add(-time.Minute)) || repo.offlineAskedAfter.After(cutoff.Add(time.Minute)) {
		t.Errorf("asked for commands older than %v, want about %v", repo.offlineAskedAfter, cutoff)
	}
}

// The case this exists for: a terminal report that never arrives on a node
// that stays connected. The reconnect sweep has already run and the
// unreachable sweep never fires, so without the clock the command would sit
// in_progress indefinitely and every action on that node would be withheld.
func TestTheBackstopSettlesACommandThatStoppedReporting(t *testing.T) {
	stuck := cmd(node_command.SlugApplyUtilityConfig, node_command.StatusInProgress)
	repo := &sweepRepo{abandoned: []node_command.NodeCommand{stuck}}
	svc, workflow, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	settled, err := svc.FailAbandonedInProgressCommands(context.Background())
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}
	if got := repo.statuses[stuck.ID]; got != string(node_command.StatusFailed) {
		t.Errorf("status = %q, want failed", got)
	}
	// The reason must describe what the platform SAW. A lost report and a
	// wedged execution are indistinguishable from here, and the device's own
	// journal is where the difference lives — so the text says to go there.
	if detail := repo.details[stuck.ID]; detail == "" || detail != reasonNoTerminalReport {
		t.Errorf("details = %q, want the no-terminal-report reason", detail)
	}
	// A workflow waiting on it must be released, or the run stays active
	// forever — the same wound the unreachable sweep closes by another route.
	if len(workflow.completions) != 1 || workflow.completions[0] != stuck.ID {
		t.Errorf("workflow completions = %v, want the settled command", workflow.completions)
	}
}

// The grace is the whole safety of a time-based sweep, so it is asserted
// rather than left to a constant nobody reads.
func TestTheBackstopAsksForCommandsSilentLongerThanTheGrace(t *testing.T) {
	repo := &sweepRepo{}
	svc, _, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	before := time.Now()
	if _, err := svc.FailAbandonedInProgressCommands(context.Background()); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	cutoff := before.Add(-abandonedSilenceGrace)
	if repo.askedAfter.Before(cutoff.Add(-time.Minute)) || repo.askedAfter.After(cutoff.Add(time.Minute)) {
		t.Errorf("asked for commands older than %v, want about %v", repo.askedAfter, cutoff)
	}
	if repo.askedLimit != abandonedSweepLimit {
		t.Errorf("limit = %d, want %d", repo.askedLimit, abandonedSweepLimit)
	}
}

// One pass must not be able to take the monitor down with it.
func TestTheBackstopReportsAReadFailureWithoutSettlingAnything(t *testing.T) {
	repo := &sweepRepo{listErr: errors.New("database down")}
	svc, _, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	settled, err := svc.FailAbandonedInProgressCommands(context.Background())
	if err == nil {
		t.Fatal("err = nil, want the read failure surfaced to the caller")
	}
	if settled != 0 {
		t.Errorf("settled = %d, want 0", settled)
	}
}

// Nothing to do is not an error, and must not log or notify.
func TestTheBackstopIsQuietWhenThereIsNothingToSettle(t *testing.T) {
	repo := &sweepRepo{}
	svc, workflow, ws := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	settled, err := svc.FailAbandonedInProgressCommands(context.Background())
	if err != nil || settled != 0 {
		t.Fatalf("got (%d, %v), want (0, nil)", settled, err)
	}
	if ws.sent != 0 || len(workflow.completions) != 0 {
		t.Errorf("an empty sweep notified: ws=%d completions=%v", ws.sent, workflow.completions)
	}
}
