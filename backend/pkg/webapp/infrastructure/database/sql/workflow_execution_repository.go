package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Verify interface implementation
var _ repository.WorkflowExecutionRepository = (*WorkflowExecutionRepository)(nil)

// WorkflowExecutionRepository is the implementation of the workflow execution repository using SQL
type WorkflowExecutionRepository struct {
	db *gorm.DB
}

// autoMigrate creates the necessary database tables
func (r *WorkflowExecutionRepository) autoMigrate() error {
	return r.db.AutoMigrate(&WorkflowExecutionModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *WorkflowExecutionRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	return r.backfillTerminality()
}

// backfillTerminality stamps completed_at on legacy rows that are in fact
// finished. The active-executions cap and the retention sweep both key off
// completed_at, so unfinished-looking legacy rows would consume cap slots
// forever and never be swept. Three shapes exist:
//   - partially_failed rows whose nodes are ALL terminal (the old code never
//     called Complete() for mixed outcomes, so completed_at stayed NULL);
//   - pending rows with NO execution nodes at all (every target id was
//     invalid; nothing will ever drive them). The 1h age guard keeps a row
//     another instance is creating right now out of reach;
//   - rows of ANY status whose nodes are all terminal but whose completed_at is
//     NULL, which is what a full-row write from a copy read before the run
//     settled used to leave behind. Update refuses those writes
//     now, but rows they already produced may still exist in a database that
//     ran the older code, and nothing else will ever settle them: their nodes are done, so no worker,
//     reconnect or command completion will look at them again.
//
// Idempotent: matched rows gain completed_at and never match again.
func (r *WorkflowExecutionRepository) backfillTerminality() error {
	// This repo's Setup runs before the execution-node repo's (main.go order),
	// so on a FRESH database workflow_execution_nodes does not exist yet. A
	// fresh install has no legacy rows to fix — skipping is correct, and the
	// backfill runs on every later start once the table exists.
	if !r.db.Migrator().HasTable("workflow_execution_nodes") {
		return nil
	}
	if err := r.db.Exec(
		`UPDATE workflow_executions SET completed_at = updated_at
		 WHERE status = ? AND completed_at IS NULL
		   AND NOT EXISTS (
		     SELECT 1 FROM workflow_execution_nodes wen
		     WHERE wen.execution_id = workflow_executions.id
		       AND wen.status IN ('pending', 'running')
		   )`,
		string(workflow_execution.StatusPartiallyFailed),
	).Error; err != nil {
		return err
	}
	if err := r.db.Exec(
		`UPDATE workflow_executions SET status = ?, completed_at = now(), updated_at = now()
		 WHERE status = ? AND completed_at IS NULL
		   AND created_at < now() - interval '1 hour'
		   AND NOT EXISTS (
		     SELECT 1 FROM workflow_execution_nodes wen
		     WHERE wen.execution_id = workflow_executions.id
		   )`,
		string(workflow_execution.StatusFailed),
		string(workflow_execution.StatusPending),
	).Error; err != nil {
		return err
	}
	// The reopened rows. Status is DERIVED from the nodes rather than kept,
	// because the stale write that cleared completed_at also put back whatever
	// status its copy held. The 1h guard on updated_at is what separates these
	// from a run whose last node settled a moment ago and whose own finalize is
	// still in flight — the only window in which "every node terminal" is not
	// yet "the run is over".
	return r.db.Exec(
		`UPDATE workflow_executions e SET completed_at = e.updated_at,
		   status = CASE
		     WHEN NOT EXISTS (
		       SELECT 1 FROM workflow_execution_nodes wen
		       WHERE wen.execution_id = e.id AND wen.status <> 'completed') THEN ?
		     WHEN NOT EXISTS (
		       SELECT 1 FROM workflow_execution_nodes wen
		       WHERE wen.execution_id = e.id AND wen.status <> 'failed') THEN ?
		     ELSE ? END
		 WHERE e.completed_at IS NULL
		   AND e.updated_at < now() - interval '1 hour'
		   AND EXISTS (
		     SELECT 1 FROM workflow_execution_nodes wen WHERE wen.execution_id = e.id)
		   AND NOT EXISTS (
		     SELECT 1 FROM workflow_execution_nodes wen
		     WHERE wen.execution_id = e.id AND wen.status IN ('pending', 'running'))`,
		string(workflow_execution.StatusCompleted),
		string(workflow_execution.StatusFailed),
		string(workflow_execution.StatusPartiallyFailed),
	).Error
}

// NewWorkflowExecutionRepository creates a new workflow execution repository
func NewWorkflowExecutionRepository(db *gorm.DB) *WorkflowExecutionRepository {
	return &WorkflowExecutionRepository{
		db: db,
	}
}

// Create creates a new workflow execution
func (r *WorkflowExecutionRepository) Create(ctx context.Context, execution *workflow_execution.WorkflowExecution) error {
	model := &WorkflowExecutionModel{}
	model.FromDomain(execution)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// Update writes every column of an existing workflow execution, and REFUSES to
// write over a run that already carries completed_at (app_errors.
// ErrExecutionAlreadyCompleted).
//
// Same shape and same reason as WorkflowExecutionNodeRepository.UpdateActive:
// every caller here holds a copy read before its own work, so a copy that
// predates a concurrent settle (an operator cancel, a node delete abandoning the
// run, another worker finishing the last node) still says pending/running with a
// nil completed_at — and a full-row write would put that back. Reopening a
// finished run is not a cosmetic loss: completed_at is what the per-account
// active-executions cap counts and what the retention sweep deletes on, so the
// row would hold a cap slot and evade retention for good, invisibly.
//
// The refusal costs nothing legitimate: a settler writes its own completed_at
// (allowed while the stored row is still open), and there is no path that
// deliberately edits a finished run.
func (r *WorkflowExecutionRepository) Update(ctx context.Context, execution *workflow_execution.WorkflowExecution) error {
	model := &WorkflowExecutionModel{}
	model.FromDomain(execution)

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// FOR UPDATE takes the row lock the competing settle contends for, so
		// the two serialize instead of racing between the read and the write.
		var current WorkflowExecutionModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "completed_at").
			Where("id = ? AND account_id = ?", execution.ID, execution.AccountID).
			First(&current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return app_errors.ErrNotFound
			}
			return err
		}
		if current.CompletedAt != nil {
			return app_errors.ErrExecutionAlreadyCompleted
		}

		result := updateAllColumns(tx.
			Where("id = ? AND account_id = ?", execution.ID, execution.AccountID), model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNotFound
		}
		return nil
	})
}

// MarkStarted moves a pending run to running, writing status and started_at and
// nothing else, and returns the stored row as it is afterwards.
//
// Two reasons for the narrow write. The engine's copy of the execution comes
// from a worker the creation loop enqueued BEFORE UpdateNodeCount landed, so a
// full-row write reverted node_count to 0 and a 50-node run reported "0 nodes"
// for the rest of its life. And the transition is claimed in SQL — the WHERE
// carries status = pending — so two workers entering begin() at once produce one
// started_at rather than whichever wrote last.
//
// The returned row is what the caller must then hold: it carries the count, and
// notifying the frontend from the pre-write copy would push the same 0 the write
// no longer does.
func (r *WorkflowExecutionRepository) MarkStarted(ctx context.Context, accountID, executionID uuid.UUID, startedAt time.Time) (*workflow_execution.WorkflowExecution, error) {
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionModel{}).
		Where("id = ? AND account_id = ? AND status = ?", executionID, accountID, string(workflow_execution.StatusPending)).
		Updates(map[string]interface{}{
			"status":     string(workflow_execution.StatusRunning),
			"started_at": startedAt,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return nil, result.Error
	}
	// RowsAffected == 0 is not an error: another worker claimed the transition,
	// or the run has already moved on. Either way the caller wants the stored
	// row, which is what it gets.
	return r.GetByID(ctx, accountID, executionID)
}

// UpdateNodeCount writes node_count alone, for the caller that has already
// handed the execution to the workers.
//
// A full-row Update cannot be used there: the workers start on the ids enqueued
// inside the creation loop, so by the time the count is known the run may have
// advanced — or finished — and the caller's copy still says pending with no
// completed_at. Naming the one column it changes is what makes the write safe
// regardless of what happened underneath it.
func (r *WorkflowExecutionRepository) UpdateNodeCount(ctx context.Context, accountID, executionID uuid.UUID, nodeCount int) error {
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionModel{}).
		Where("id = ? AND account_id = ?", executionID, accountID).
		Updates(map[string]interface{}{
			"node_count": nodeCount,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNotFound
	}
	return nil
}

// GetByID retrieves a workflow execution by ID within an account
func (r *WorkflowExecutionRepository) GetByID(ctx context.Context, accountID, executionID uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	var model WorkflowExecutionModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", executionID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// List returns one page of the account's executions, newest first, plus the
// total count for the filter. template_snapshot is omitted — each row carries a
// large jsonb and a per-minute cron grows this table without bound; the full
// snapshot is only returned by the single-execution GET.
func (r *WorkflowExecutionRepository) List(ctx context.Context, accountID uuid.UUID, filter repository.WorkflowExecutionListFilter) ([]workflow_execution.WorkflowExecution, int64, error) {
	q := r.db.WithContext(ctx).
		Model(&WorkflowExecutionModel{}).
		Where("account_id = ?", accountID)
	if filter.TemplateID != nil {
		q = q.Where("template_id = ?", *filter.TemplateID)
	}
	if len(filter.NodeOUIDs) > 0 {
		// Same semantics as the Runs page OU rail: a run matches when at least
		// one of its target nodes is currently assigned to one of the OUs.
		q = q.Where(
			"id IN (SELECT wen.execution_id FROM workflow_execution_nodes wen JOIN nodes n ON n.id = wen.node_id WHERE wen.account_id = ? AND n.ou_id IN ?)",
			accountID, filter.NodeOUIDs,
		)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q = q.Omit("template_snapshot").Order("created_at DESC")
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit).Offset(filter.Offset)
	}

	var models []WorkflowExecutionModel
	if err := q.Find(&models).Error; err != nil {
		return nil, 0, err
	}

	executions := make([]workflow_execution.WorkflowExecution, len(models))
	for i, model := range models {
		executions[i] = *model.ToDomain()
	}
	return executions, total, nil
}

// CountActiveByAccountID counts the account's unfinished executions. "Active"
// is completed_at IS NULL, NOT a status list: partially_failed is set while
// nodes are still running (transient) and only gains completed_at once every
// node is terminal — a status-based count would let a run with one dead node
// slip out of the cap while the rest keeps executing.
func (r *WorkflowExecutionRepository) CountActiveByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionModel{}).
		Where("account_id = ? AND completed_at IS NULL", accountID).
		Count(&count)
	if result.Error != nil {
		return 0, result.Error
	}
	return count, nil
}

// ListExpiredIDs returns up to limit ids of executions FINISHED before cutoff
// (completed_at, not created_at: a long-running run that finished yesterday
// keeps its full retention window), oldest first (one retention batch).
// Unfinished executions (completed_at IS NULL) are never returned.
func (r *WorkflowExecutionRepository) ListExpiredIDs(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionModel{}).
		Where("completed_at IS NOT NULL AND completed_at < ?", cutoff).
		Order("completed_at ASC").
		Limit(limit).
		Pluck("id", &ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return ids, nil
}

// DeleteByIDs deletes executions by id.
func (r *WorkflowExecutionRepository) DeleteByIDs(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Where("id IN ?", ids).
		Delete(&WorkflowExecutionModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
