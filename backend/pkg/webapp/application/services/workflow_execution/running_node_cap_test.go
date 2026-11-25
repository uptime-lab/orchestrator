package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

// --- the per-account running-node cap --------------------------------------

// capExecNodeRepo answers the running-node count and nothing else.
type capExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	running int64
	err     error
	asked   int
}

func (r *capExecNodeRepo) CountActiveByAccountID(_ context.Context, _ uuid.UUID) (int64, error) {
	r.asked++
	return r.running, r.err
}

func capService(nodes *capExecNodeRepo, cap int) *Service {
	return &Service{
		executionNodeRepo: nodes,
		// A template repo that answers nothing: reaching it means the cap let the
		// start through, which is what the "allowed" cases assert.
		templateRepo: &stubTemplateRepo{},
		cfg:          config.WorkflowConfig{MaxRunningNodesPerAccount: cap},
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func startCmd(nodeCount int) workflow_execution_start.WorkflowExecutionStartCommand {
	ids := make([]uuid.UUID, nodeCount)
	for i := range ids {
		ids[i] = uuid.New()
	}
	return workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  uuid.New(),
		NodeIDs:     ids,
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	}
}

// The cap counts the REQUESTED nodes too, not just what is already running.
// Checking only the current total would let a single run over a large fleet
// blow straight past it — and it must refuse before an execution row exists, or
// the account is left with a partial run nobody asked for.
func TestTheRunningNodeCapCountsTheRequestedNodes(t *testing.T) {
	nodes := &capExecNodeRepo{running: 8}
	_, err := capService(nodes, 10).StartExecution(context.Background(), startCmd(5))
	if !errors.Is(err, app_errors.ErrTooManyRunningNodes) {
		t.Fatalf("8 running + 5 requested against a cap of 10 returned %v, want the cap refusal", err)
	}
}

// Exactly at the cap is allowed; one over is not. Stated as a test because an
// off-by-one here is the difference between a guard and a product limit.
func TestTheRunningNodeCapBoundaryIsInclusive(t *testing.T) {
	atCap := &capExecNodeRepo{running: 5}
	_, err := capService(atCap, 10).StartExecution(context.Background(), startCmd(5))
	if errors.Is(err, app_errors.ErrTooManyRunningNodes) {
		t.Error("a start landing exactly on the cap was refused")
	}

	overCap := &capExecNodeRepo{running: 6}
	_, err = capService(overCap, 10).StartExecution(context.Background(), startCmd(5))
	if !errors.Is(err, app_errors.ErrTooManyRunningNodes) {
		t.Errorf("a start one node over the cap returned %v, want the refusal", err)
	}
}

// 0 disables it, like every other quota guard.
func TestTheRunningNodeCapIsOffAtZero(t *testing.T) {
	nodes := &capExecNodeRepo{running: 100000}
	_, err := capService(nodes, 0).StartExecution(context.Background(), startCmd(50))
	if errors.Is(err, app_errors.ErrTooManyRunningNodes) {
		t.Error("the cap refused a start while disabled")
	}
	if nodes.asked != 0 {
		t.Errorf("the count was queried %d times with the cap off", nodes.asked)
	}
}

// A transient count failure must not stop legitimate work: the cap protects
// scale, and availability of a real run wins over enforcing it on a database
// hiccup. Same posture as the active-execution cap beside it.
func TestACountFailureLetsTheStartThrough(t *testing.T) {
	nodes := &capExecNodeRepo{err: errors.New("database is unhappy")}
	_, err := capService(nodes, 10).StartExecution(context.Background(), startCmd(5))
	if errors.Is(err, app_errors.ErrTooManyRunningNodes) {
		t.Error("a start was refused because the cap check itself failed")
	}
}
