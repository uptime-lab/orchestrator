package node_command

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

type rebootRepo struct {
	repository.NodeCommandRepository
	pending  []node_command.NodeCommand
	statuses map[uuid.UUID]string
}

func (r *rebootRepo) GetInProgressRebootCommandsByNodeID(context.Context, uuid.UUID) ([]node_command.NodeCommand, error) {
	return r.pending, nil
}

func (r *rebootRepo) UpdateStatus(_ context.Context, id uuid.UUID, status node_command.NodeCommandStatus, _ int, _ string) error {
	if r.statuses == nil {
		r.statuses = map[uuid.UUID]string{}
	}
	r.statuses[id] = string(status)
	return nil
}

func (r *rebootRepo) GetByIDForUpdate(_ context.Context, id uuid.UUID) (*node_command.NodeCommand, error) {
	for _, c := range r.pending {
		if c.ID == id {
			copied := c
			return &copied, nil
		}
	}
	return nil, nil
}

func (r *rebootRepo) GetWorkflowExecutionNodeID(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}

// Nothing else is running on the node, so the completion path releases it.
func (r *rebootRepo) HasActiveCommands(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

// bootNodeService answers only the read the completion path makes. A nil
// LastBootAt is how an agent too old to report uptime looks.
type bootNodeService struct {
	services.NodeService
	lastBootAt *time.Time
}

func (s *bootNodeService) GetByIDAndAccount(_ context.Context, id, accountID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: id, AccountID: accountID, LastBootAt: s.lastBootAt}, nil
}

// The release back out of `restarting` after a settled batch.
func (s *bootNodeService) UpdateStatusWithAccount(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func rebootFixture(issuedAt time.Time, lastBoot *time.Time) (*Service, *rebootRepo, node_command.NodeCommand) {
	reboot := node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      node_command.SlugReboot,
		Status:    node_command.StatusSent,
		CreatedAt: issuedAt,
	}
	repo := &rebootRepo{pending: []node_command.NodeCommand{reboot}}
	recorder, _ := audittest.New()
	svc := NewService(repo, &stubWebsocket{}, recorder, slog.Default())
	svc.SetNodeService(&bootNodeService{lastBootAt: lastBoot})
	return svc, repo, reboot
}

// A reconnect is not a reboot.
//
// reboot accepts a delay of up to an hour, and ANY reconnect used to settle the
// command: a backend deploy that brings the fleet back marked it success,
// released the `restarting` pin and let the playbook continue — and the device
// then rebooted up to an hour later, mid-run.
func TestAReconnectWithoutABootDoesNotCompleteTheReboot(t *testing.T) {
	issued := time.Now().Add(-2 * time.Minute)
	bootedBefore := issued.Add(-time.Hour) // the node's last boot predates the command
	svc, repo, reboot := rebootFixture(issued, &bootedBefore)

	if err := svc.CompleteRebootCommandsForNode(context.Background(), reboot.NodeID); err != nil {
		t.Fatalf("CompleteRebootCommandsForNode: %v", err)
	}
	if got := repo.statuses[reboot.ID]; got != "" {
		t.Fatalf("the reboot was marked %q on a reconnect the device never rebooted for", got)
	}
}

// Once the device really has booted, the command completes.
func TestABootAfterTheCommandCompletesIt(t *testing.T) {
	issued := time.Now().Add(-10 * time.Minute)
	bootedAfter := issued.Add(time.Minute)
	svc, repo, reboot := rebootFixture(issued, &bootedAfter)

	if err := svc.CompleteRebootCommandsForNode(context.Background(), reboot.NodeID); err != nil {
		t.Fatalf("CompleteRebootCommandsForNode: %v", err)
	}
	if got := repo.statuses[reboot.ID]; got != string(node_command.StatusSuccess) {
		t.Fatalf("status = %q, want success: the node booted after the command was issued", got)
	}
}

// An agent too old to report uptime keeps the old behaviour. Fail-closed there
// would hang every reboot on those nodes forever, with no operator remedy.
func TestANodeThatReportsNoBootMomentStillCompletes(t *testing.T) {
	issued := time.Now().Add(-time.Minute)
	svc, repo, reboot := rebootFixture(issued, nil)

	if err := svc.CompleteRebootCommandsForNode(context.Background(), reboot.NodeID); err != nil {
		t.Fatalf("CompleteRebootCommandsForNode: %v", err)
	}
	if got := repo.statuses[reboot.ID]; got != string(node_command.StatusSuccess) {
		t.Fatalf("status = %q, want success: an unknown boot moment must not hang the command", got)
	}
}
