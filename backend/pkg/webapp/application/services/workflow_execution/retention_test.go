package workflow_execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/google/uuid"
)

// The stubs embed their interface so only the methods a test exercises need
// implementations; an unexpected call panics via the nil embedded interface.

type stubExecutionRepo struct {
	repository.WorkflowExecutionRepository
	activeCount    int64
	expiredBatches [][]uuid.UUID // consumed by ListExpiredIDs, one per call
	deletedIDs     [][]uuid.UUID
	updated        []*workflow_execution.WorkflowExecution
}

func (s *stubExecutionRepo) Update(ctx context.Context, execution *workflow_execution.WorkflowExecution) error {
	s.updated = append(s.updated, execution)
	return nil
}

func (s *stubExecutionRepo) CountActiveByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	return s.activeCount, nil
}

func (s *stubExecutionRepo) ListExpiredIDs(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error) {
	if len(s.expiredBatches) == 0 {
		return nil, nil
	}
	batch := s.expiredBatches[0]
	s.expiredBatches = s.expiredBatches[1:]
	return batch, nil
}

func (s *stubExecutionRepo) DeleteByIDs(ctx context.Context, ids []uuid.UUID) (int64, error) {
	s.deletedIDs = append(s.deletedIDs, ids)
	return int64(len(ids)), nil
}

type stubExecutionNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	nodeIDs          []uuid.UUID
	deletedExecCalls int
	executionNodes   []workflow_execution_node.WorkflowExecutionNode
}

func (s *stubExecutionNodeRepo) GetByExecutionID(ctx context.Context, executionID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	return s.executionNodes, nil
}

func (s *stubExecutionNodeRepo) GetIDsByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) ([]uuid.UUID, error) {
	return s.nodeIDs, nil
}

func (s *stubExecutionNodeRepo) DeleteByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) (int64, error) {
	s.deletedExecCalls++
	return int64(len(executionIDs)), nil
}

type stubNodeCommandRepo struct {
	repository.NodeCommandRepository
	deletedNodeIDs [][]uuid.UUID
}

func (s *stubNodeCommandRepo) DeleteByWorkflowExecutionNodeIDs(ctx context.Context, ids []uuid.UUID) (int64, error) {
	s.deletedNodeIDs = append(s.deletedNodeIDs, ids)
	return int64(len(ids)), nil
}

func makeIDs(n int) []uuid.UUID {
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
	}
	return ids
}

func TestRunRetentionSweepsInBatchesUntilDry(t *testing.T) {
	execRepo := &stubExecutionRepo{
		expiredBatches: [][]uuid.UUID{makeIDs(retentionBatchSize), makeIDs(3)},
	}
	nodeRepo := &stubExecutionNodeRepo{nodeIDs: makeIDs(2)}
	cmdRepo := &stubNodeCommandRepo{}

	svc := &Service{
		executionRepo:     execRepo,
		executionNodeRepo: nodeRepo,
		nodeCommandRepo:   cmdRepo,
		cfg:               config.WorkflowConfig{ExecutionRetentionDays: 30},
		logger:            slog.Default(),
	}

	svc.runRetention(context.Background())

	// A full first batch means another round; the short second batch ends the
	// sweep. Children are deleted once per batch, before their executions.
	if got := len(execRepo.deletedIDs); got != 2 {
		t.Fatalf("expected 2 delete batches, got %d", got)
	}
	if got := len(execRepo.deletedIDs[0]); got != retentionBatchSize {
		t.Fatalf("expected first batch of %d, got %d", retentionBatchSize, got)
	}
	if got := len(execRepo.deletedIDs[1]); got != 3 {
		t.Fatalf("expected second batch of 3, got %d", got)
	}
	if nodeRepo.deletedExecCalls != 2 || len(cmdRepo.deletedNodeIDs) != 2 {
		t.Fatalf("expected children swept once per batch, got nodes=%d commands=%d",
			nodeRepo.deletedExecCalls, len(cmdRepo.deletedNodeIDs))
	}
}

// A mixed outcome with EVERY node terminal must COMPLETE the execution
// (completed_at set): the active cap and retention key off completed_at, so a
// status-only partially_failed would hold a cap slot forever and never be
// swept. A mixed outcome with nodes still running must stay transient.
func TestUpdateExecutionStatusMixedOutcomeTerminality(t *testing.T) {
	wenWith := func(statuses ...workflow_execution_node.WorkflowExecutionNodeStatus) []workflow_execution_node.WorkflowExecutionNode {
		nodes := make([]workflow_execution_node.WorkflowExecutionNode, len(statuses))
		for i, st := range statuses {
			nodes[i] = workflow_execution_node.WorkflowExecutionNode{ID: uuid.New(), Status: st}
		}
		return nodes
	}

	cases := []struct {
		name          string
		nodes         []workflow_execution_node.WorkflowExecutionNode
		wantStatus    workflow_execution.WorkflowExecutionStatus
		wantCompleted bool
	}{
		{
			name: "all terminal, mixed -> completed partially_failed",
			nodes: wenWith(
				workflow_execution_node.StatusCompleted,
				workflow_execution_node.StatusFailed,
			),
			wantStatus:    workflow_execution.StatusPartiallyFailed,
			wantCompleted: true,
		},
		{
			name: "failed node with another still running -> transient partially_failed",
			nodes: wenWith(
				workflow_execution_node.StatusFailed,
				workflow_execution_node.StatusRunning,
			),
			wantStatus:    workflow_execution.StatusPartiallyFailed,
			wantCompleted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execRepo := &stubExecutionRepo{}
			svc := &Service{
				executionRepo:     execRepo,
				executionNodeRepo: &stubExecutionNodeRepo{executionNodes: tc.nodes},
				logger:            slog.Default(),
			}
			execution := &workflow_execution.WorkflowExecution{ID: uuid.New(), Status: workflow_execution.StatusRunning}

			svc.updateExecutionStatus(context.Background(), execution)

			if execution.Status != tc.wantStatus {
				t.Fatalf("expected status %s, got %s", tc.wantStatus, execution.Status)
			}
			if got := execution.CompletedAt != nil; got != tc.wantCompleted {
				t.Fatalf("expected completed=%v, got completed_at=%v", tc.wantCompleted, execution.CompletedAt)
			}
			if len(execRepo.updated) != 1 {
				t.Fatalf("expected one repo update, got %d", len(execRepo.updated))
			}
		})
	}
}

func TestStartExecutionRejectsAtActiveCap(t *testing.T) {
	accountID := uuid.New()
	svc := &Service{
		executionRepo: &stubExecutionRepo{activeCount: 5},
		cfg:           config.WorkflowConfig{MaxActiveExecutionsPerAccount: 5},
		logger:        slog.Default(),
	}

	cmd := workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  uuid.New(),
		NodeIDs:     []uuid.UUID{uuid.New()},
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}

	_, err := svc.StartExecution(context.Background(), cmd)
	if !errors.Is(err, app_errors.ErrTooManyActiveExecutions) {
		t.Fatalf("expected ErrTooManyActiveExecutions, got %v", err)
	}
}
