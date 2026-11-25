package sql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
)

// These need a real Postgres: the defect they cover is entirely in what the
// driver does when an UPDATE matches no row, which no in-process fake reproduces.
func executionRepoForTest(t *testing.T) (*WorkflowExecutionRepository, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS workflow_executions CASCADE`).Error; err != nil {
		t.Fatalf("dropping executions: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS workflow_execution_nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping execution nodes: %v", err)
	}
	if err := db.AutoMigrate(&WorkflowExecutionModel{}, &WorkflowExecutionNodeModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return NewWorkflowExecutionRepository(db), db
}

func newTestExecution(accountID uuid.UUID) *workflow_execution.WorkflowExecution {
	return workflow_execution.NewWorkflowExecution(uuid.New(), accountID)
}

// The retention sweep deletes finished runs. A worker holding a copy of one used
// to re-insert it: gorm's Save falls back to an ON CONFLICT insert when its
// UPDATE matches nothing, so the row came back — and, because the insert reports
// one row affected, the caller was told the update succeeded instead of getting
// ErrNotFound.
func TestUpdatingADeletedExecutionDoesNotRecreateIt(t *testing.T) {
	repo, db := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := repo.DeleteByIDs(ctx, []uuid.UUID{execution.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	execution.Start()
	err := repo.Update(ctx, execution)
	if !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("update of a deleted execution returned %v, want ErrNotFound", err)
	}

	var count int64
	if err := db.Model(&WorkflowExecutionModel{}).Where("id = ?", execution.ID).Count(&count).Error; err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Errorf("the deleted execution is back in the table (%d rows)", count)
	}
}

// The zombie run. A cancel (or a node delete abandoning the run) stamps
// completed_at; a worker that read the execution before that still holds a copy
// saying running with completed_at nil, and writing it whole cleared the field
// the active-executions cap counts and the retention sweep keys on — one cap
// slot consumed for good, and the operator's cancel silently undone.
func TestUpdatingWithAStaleCopyCannotReopenACompletedExecution(t *testing.T) {
	repo, _ := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The copy a worker read before the run settled.
	stale, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("read the pre-settle copy: %v", err)
	}

	settled, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	settled.Complete(workflow_execution.StatusPartiallyFailed)
	if err := repo.Update(ctx, settled); err != nil {
		t.Fatalf("settle: %v", err)
	}

	stale.Start()
	if err := repo.Update(ctx, stale); !errors.Is(err, app_errors.ErrExecutionAlreadyCompleted) {
		t.Errorf("the stale write returned %v, want ErrExecutionAlreadyCompleted", err)
	}

	got, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at was cleared: the run is active again and will never be swept")
	}
	if got.Status != workflow_execution.StatusPartiallyFailed {
		t.Errorf("status = %q, want the settled outcome %q", got.Status, workflow_execution.StatusPartiallyFailed)
	}

	active, err := repo.CountActiveByAccountID(ctx, accountID)
	if err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 0 {
		t.Errorf("the account still holds %d active executions after the run finished", active)
	}
}

// The node count is written after the workers have already been handed the run,
// so it must touch node_count and nothing else.
func TestUpdateNodeCountLeavesASettledRunAlone(t *testing.T) {
	repo, _ := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}
	settled, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	settled.Complete(workflow_execution.StatusCompleted)
	if err := repo.Update(ctx, settled); err != nil {
		t.Fatalf("settle: %v", err)
	}

	if err := repo.UpdateNodeCount(ctx, accountID, execution.ID, 3); err != nil {
		t.Fatalf("update node count: %v", err)
	}

	got, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.NodeCount != 3 {
		t.Errorf("node_count = %d, want 3", got.NodeCount)
	}
	if got.CompletedAt == nil || got.Status != workflow_execution.StatusCompleted {
		t.Errorf("the node-count write disturbed the outcome: status %q, completed_at %v", got.Status, got.CompletedAt)
	}
}

// UpdateNodeCount is account-scoped like every other write here.
func TestUpdateNodeCountRefusesAnotherAccountsExecution(t *testing.T) {
	repo, _ := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.UpdateNodeCount(ctx, uuid.New(), execution.ID, 7); !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("cross-account node-count write returned %v, want ErrNotFound", err)
	}
}

// The rows the defect already produced: every node terminal, completed_at NULL,
// nothing left to settle them. Setup() must finish them, or they hold a cap slot
// forever.
func TestSetupSettlesAReopenedExecutionWhoseNodesAreAllTerminal(t *testing.T) {
	repo, db := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&WorkflowExecutionModel{}).Where("id = ?", execution.ID).
		Updates(map[string]interface{}{"status": "running", "updated_at": old}).Error; err != nil {
		t.Fatalf("ageing the row: %v", err)
	}
	nodes := []WorkflowExecutionNodeModel{
		{ID: uuid.New(), ExecutionID: execution.ID, NodeID: uuid.New(), AccountID: accountID, Status: "completed"},
		{ID: uuid.New(), ExecutionID: execution.ID, NodeID: uuid.New(), AccountID: accountID, Status: "failed"},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatalf("creating execution nodes: %v", err)
	}

	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.CompletedAt == nil {
		t.Error("a run whose nodes are all terminal was left unfinished")
	}
	if got.Status != workflow_execution.StatusPartiallyFailed {
		t.Errorf("status = %q, want %q derived from one completed + one failed node",
			got.Status, workflow_execution.StatusPartiallyFailed)
	}
}

// The same shape while a node is still running is NOT the defect — it is an
// ordinary in-flight run, and settling it would kill live work.
func TestSetupLeavesARunWithALiveNodeAlone(t *testing.T) {
	repo, db := executionRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	execution := newTestExecution(accountID)
	if err := repo.Create(ctx, execution); err != nil {
		t.Fatalf("create: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&WorkflowExecutionModel{}).Where("id = ?", execution.ID).
		Updates(map[string]interface{}{"status": "running", "updated_at": old}).Error; err != nil {
		t.Fatalf("ageing the row: %v", err)
	}
	nodes := []WorkflowExecutionNodeModel{
		{ID: uuid.New(), ExecutionID: execution.ID, NodeID: uuid.New(), AccountID: accountID, Status: "completed"},
		{ID: uuid.New(), ExecutionID: execution.ID, NodeID: uuid.New(), AccountID: accountID, Status: "running"},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatalf("creating execution nodes: %v", err)
	}

	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := repo.GetByID(ctx, accountID, execution.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.CompletedAt != nil {
		t.Error("an execution with a running node was force-finished")
	}
}
