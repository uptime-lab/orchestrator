package node_command

import (
	"context"
	"errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	websockets2 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// The stubs embed their interface, so only the methods this path uses need an
// implementation and an unexpected call panics on the nil embedded interface —
// which is itself part of the assertion: settling an unreachable node's
// commands must not reach for the node service.

type stubCommandRepo struct {
	repository.NodeCommandRepository

	pending  []node_command.NodeCommand
	listErr  error
	statuses map[uuid.UUID]string
	details  map[uuid.UUID]string
	failWith error
}

func (s *stubCommandRepo) GetStalePendingCommandsByNodeID(context.Context, uuid.UUID) ([]node_command.NodeCommand, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.pending, nil
}

func (s *stubCommandRepo) UpdateStatus(_ context.Context, id uuid.UUID, status node_command.NodeCommandStatus, _ int, details string) error {
	if s.failWith != nil {
		return s.failWith
	}
	if s.statuses == nil {
		s.statuses = map[uuid.UUID]string{}
		s.details = map[uuid.UUID]string{}
	}
	s.statuses[id] = string(status)
	s.details[id] = details
	return nil
}

func (s *stubCommandRepo) GetByIDForUpdate(_ context.Context, id uuid.UUID) (*node_command.NodeCommand, error) {
	for _, cmd := range s.pending {
		if cmd.ID == id {
			copied := cmd
			copied.Status = node_command.StatusFailed
			return &copied, nil
		}
	}
	return nil, errors.New("not found")
}

func (s *stubCommandRepo) GetWorkflowExecutionNodeID(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}

type stubWebsocket struct {
	sent int
}

func (s *stubWebsocket) Send(websockets2.Message) error       { s.sent++; return nil }
func (s *stubWebsocket) GetChannel() chan websockets2.Message { return nil }

type stubWorkflowService struct {
	completions []uuid.UUID
}

func (s *stubWorkflowService) ProcessNodeCommandCompletion(_ context.Context, id uuid.UUID) {
	s.completions = append(s.completions, id)
}

func unreachableService(repo *stubCommandRepo) (*Service, *stubWorkflowService, *stubWebsocket) {
	ws := &stubWebsocket{}
	workflow := &stubWorkflowService{}
	recorder, _ := audittest.New()
	svc := NewService(repo, ws, recorder, slog.Default())
	svc.SetWorkflowExecutionService(workflow)
	return svc, workflow, ws
}

func cmd(slug string, status node_command.NodeCommandStatus) node_command.NodeCommand {
	return node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      slug,
		Status:    status,
	}
}

// The failure mode of the most-used command in the product must have an
// outcome. Before this, a reboot the node never returned from stayed
// non-terminal forever.
func TestARebootTheNodeNeverReturnedFromIsFailedAndSaysSo(t *testing.T) {
	reboot := cmd(node_command.SlugReboot, node_command.StatusInProgress)
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{reboot}}
	svc, _, _ := unreachableService(repo)

	failed, err := svc.FailCommandsForUnreachableNode(context.Background(), reboot.NodeID)
	if err != nil {
		t.Fatalf("settling: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed %d commands, want 1", failed)
	}
	if repo.statuses[reboot.ID] != string(node_command.StatusFailed) {
		t.Fatalf("status is %q, want failed", repo.statuses[reboot.ID])
	}
	if repo.details[reboot.ID] != reasonRebootNeverReturned {
		t.Fatalf("detail is %q; an operator has to be able to tell this apart from a generic failure", repo.details[reboot.ID])
	}
}

// For anything else the platform knows only that the device went quiet, and the
// wording must not claim more than that.
func TestANonRebootCommandGetsTheObservationNotTheDiagnosis(t *testing.T) {
	install := cmd(node_command.SlugInstallOS, node_command.StatusInProgress)
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{install}}
	svc, _, _ := unreachableService(repo)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), install.NodeID); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if repo.details[install.ID] != reasonNodeStoppedAnswering {
		t.Fatalf("detail is %q, want the observation wording", repo.details[install.ID])
	}
}

// Without this a workflow execution stays active forever on a node that is
// gone, counting against the per-account cap with no route to cancel it.
func TestTheWorkflowEngineIsToldTheCommandIsOver(t *testing.T) {
	reboot := cmd(node_command.SlugReboot, node_command.StatusInProgress)
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{reboot}}
	svc, workflow, _ := unreachableService(repo)

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), reboot.NodeID); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if len(workflow.completions) != 1 || workflow.completions[0] != reboot.ID {
		t.Fatalf("workflow completions %v, want exactly the settled command", workflow.completions)
	}
}

// The node service is deliberately not consulted: its "no active commands left,
// so back to online" rule is right after a reconnect and would resurrect a
// corpse here. The embedded nil NodeService would panic if this reached for it.
func TestSettlingDoesNotTouchTheNodeStatus(t *testing.T) {
	reboot := cmd(node_command.SlugReboot, node_command.StatusInProgress)
	repo := &stubCommandRepo{pending: []node_command.NodeCommand{reboot}}
	svc, _, _ := unreachableService(repo)
	// No SetNodeService call: any use of it panics.

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), reboot.NodeID); err != nil {
		t.Fatalf("settling: %v", err)
	}
}

func TestNothingPendingIsNotAnError(t *testing.T) {
	svc, _, ws := unreachableService(&stubCommandRepo{})

	failed, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New())
	if err != nil || failed != 0 {
		t.Fatalf("got (%d, %v), want (0, nil)", failed, err)
	}
	if ws.sent != 0 {
		t.Fatal("emitted a websocket event with nothing to settle")
	}
}

// One command that cannot be written must not abandon the rest: a node that
// died mid-workflow has several outstanding at once.
func TestOneUnwritableCommandDoesNotStopTheOthers(t *testing.T) {
	first := cmd(node_command.SlugReboot, node_command.StatusInProgress)
	second := cmd(node_command.SlugStatusCheck, node_command.StatusSent)
	repo := &failFirstRepo{stubCommandRepo: stubCommandRepo{pending: []node_command.NodeCommand{first, second}}, failFor: first.ID}
	svc, _, _ := unreachableService(&repo.stubCommandRepo)
	svc.repo = repo

	failed, err := svc.FailCommandsForUnreachableNode(context.Background(), first.NodeID)
	if err != nil {
		t.Fatalf("settling: %v", err)
	}
	if failed != 1 {
		t.Fatalf("settled %d commands, want the one that could be written", failed)
	}
	if repo.statuses[second.ID] != string(node_command.StatusFailed) {
		t.Fatal("the second command was skipped because the first could not be written")
	}
}

type failFirstRepo struct {
	stubCommandRepo
	failFor uuid.UUID
}

func (f *failFirstRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status node_command.NodeCommandStatus, progress int, details string) error {
	if id == f.failFor {
		return errors.New("write conflict")
	}
	return f.stubCommandRepo.UpdateStatus(ctx, id, status, progress, details)
}

func TestAListFailurePropagates(t *testing.T) {
	svc, _, _ := unreachableService(&stubCommandRepo{listErr: errors.New("database is down")})

	if _, err := svc.FailCommandsForUnreachableNode(context.Background(), uuid.New()); err == nil {
		t.Fatal("swallowed a repository error; the caller retries on the next tick and must know")
	}
}
