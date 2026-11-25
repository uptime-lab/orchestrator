package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Verify interface implementation
var _ repository.WorkflowExecutionNodeRepository = (*WorkflowExecutionNodeRepository)(nil)

// WorkflowExecutionNodeRepository is the implementation of the workflow execution node repository using SQL
type WorkflowExecutionNodeRepository struct {
	db *gorm.DB
	// encryptor protects block outputs (stdout, file contents, HTTP bodies) at
	// rest; see encryptGraphStateValues.
	encryptor commandFieldEncryptor
}

// autoMigrate creates the necessary database tables
func (r *WorkflowExecutionNodeRepository) autoMigrate() error {
	return r.db.AutoMigrate(&WorkflowExecutionNodeModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *WorkflowExecutionNodeRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	// One-time (idempotent) encryption of legacy plaintext block outputs; runs
	// before the servers accept traffic, so it cannot race live updates.
	return backfillGraphStateEncryption(r.db, r.encryptor)
}

// NewWorkflowExecutionNodeRepository creates a new workflow execution node repository
func NewWorkflowExecutionNodeRepository(db *gorm.DB, encryptor commandFieldEncryptor) *WorkflowExecutionNodeRepository {
	if encryptor == nil {
		panic("WorkflowExecutionNodeRepository requires an encryptor")
	}
	return &WorkflowExecutionNodeRepository{
		db:        db,
		encryptor: encryptor,
	}
}

// Create creates a new workflow execution node
func (r *WorkflowExecutionNodeRepository) Create(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) error {
	model := &WorkflowExecutionNodeModel{}
	if err := model.FromDomain(node, r.encryptor); err != nil {
		return err
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// Update updates an existing workflow execution node
func (r *WorkflowExecutionNodeRepository) Update(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) error {
	model := &WorkflowExecutionNodeModel{}
	if err := model.FromDomain(node, r.encryptor); err != nil {
		return err
	}

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ?", node.ID), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNotFound
	}

	return nil
}

// UpdateActive persists the node ONLY while the stored row is still pending or
// running, and reports whether it wrote. It returns false without writing when
// the row has already reached a terminal state — which is exactly the case where
// a concurrent CancelExecution or AbandonNodeExecutions settled it while a worker
// held a stale, pre-settle in-memory copy. Without this guard the worker's
// finalize would resurrect a cancelled/abandoned run to `running` via
// last-write-wins, re-consume one of the per-account active-execution slots, and
// make the operator's cancel a silent no-op (its own completed_at is already set,
// so the second cancel does nothing).
//
// A legitimate write is never rejected: the worker only ever persists over a row
// it left pending/running, so the guard fires solely when something else has
// already reached a terminal state underneath it.
func (r *WorkflowExecutionNodeRepository) UpdateActive(ctx context.Context, node *workflow_execution_node.WorkflowExecutionNode) (bool, error) {
	model := &WorkflowExecutionNodeModel{}
	if err := model.FromDomain(node, r.encryptor); err != nil {
		return false, err
	}
	model.UpdatedAt = time.Now()

	// A full-row write updates by primary key and does NOT honour an extra WHERE
	// on a non-key column, so the guard has to be a locked read-then-write in one
	// transaction. SELECT ... FOR UPDATE takes the row lock the settling Update
	// contends for, so the two serialize: whichever commits a terminal status
	// first wins, and a worker arriving after a settle sees the terminal row and
	// declines to write.
	wrote := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current WorkflowExecutionNodeModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status").
			Where("id = ?", node.ID).
			First(&current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // the row is gone; nothing to resurrect
			}
			return err
		}
		if current.Status != string(workflow_execution_node.StatusPending) &&
			current.Status != string(workflow_execution_node.StatusRunning) {
			return nil // already terminal — refuse, leaving the settled outcome intact
		}
		if err := updateAllColumns(tx.Where("id = ?", node.ID), model).Error; err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// GetByID retrieves a workflow execution node by ID
func (r *WorkflowExecutionNodeRepository) GetByID(ctx context.Context, executionNodeID uuid.UUID) (*workflow_execution_node.WorkflowExecutionNode, error) {
	var model WorkflowExecutionNodeModel
	result := r.db.WithContext(ctx).
		Where("id = ?", executionNodeID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(r.encryptor)
}

// GetByExecutionID retrieves all workflow execution nodes for a specific execution
func (r *WorkflowExecutionNodeRepository) GetByExecutionID(ctx context.Context, executionID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	var models []WorkflowExecutionNodeModel
	result := r.db.WithContext(ctx).
		Where("execution_id = ?", executionID).
		Order("created_at ASC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	nodes := make([]workflow_execution_node.WorkflowExecutionNode, len(models))
	for i, model := range models {
		domainNode, err := model.ToDomain(r.encryptor)
		if err != nil {
			return nil, err
		}
		nodes[i] = *domainNode
	}

	return nodes, nil
}

// GetActiveByNodeID retrieves the unfinished (pending/running) workflow
// execution nodes of a specific node. Filtered in SQL, not by the caller: the
// finished ones are exactly what makes this table grow without bound.
func (r *WorkflowExecutionNodeRepository) GetActiveByNodeID(ctx context.Context, accountID, nodeID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	var models []WorkflowExecutionNodeModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND account_id = ? AND status IN ?", nodeID, accountID,
			[]string{
				string(workflow_execution_node.StatusPending),
				string(workflow_execution_node.StatusRunning),
			}).
		Order("created_at DESC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	nodes := make([]workflow_execution_node.WorkflowExecutionNode, len(models))
	for i, model := range models {
		domainNode, err := model.ToDomain(r.encryptor)
		if err != nil {
			return nil, err
		}
		nodes[i] = *domainNode
	}

	return nodes, nil
}

// FindIDByCurrentCommandID returns the id of the execution node whose current
// device command is commandID, or uuid.Nil when none is. Newest first, because
// current_command_id is not unique across history: a command id belongs to one
// command, but a retried block can leave the same id recorded on an older row.
func (r *WorkflowExecutionNodeRepository) FindIDByCurrentCommandID(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Where("account_id = ? AND node_id = ? AND current_command_id = ?", accountID, nodeID, commandID).
		Order("created_at DESC").
		Limit(1).
		Pluck("id", &ids).Error
	if err != nil {
		return uuid.Nil, err
	}
	if len(ids) == 0 {
		return uuid.Nil, nil
	}
	return ids[0], nil
}

// CountActiveByAccountID counts an account's pending+running execution nodes —
// the account's actual share of the worker pool.
func (r *WorkflowExecutionNodeRepository) CountActiveByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Where("account_id = ? AND status IN ?", accountID,
			[]string{
				string(workflow_execution_node.StatusPending),
				string(workflow_execution_node.StatusRunning),
			}).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CountByExecutionID counts the number of nodes for a specific execution
func (r *WorkflowExecutionNodeRepository) CountByExecutionID(ctx context.Context, executionID uuid.UUID) (int64, error) {
	var count int64
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Where("execution_id = ?", executionID).
		Count(&count)
	if result.Error != nil {
		return 0, result.Error
	}
	return count, nil
}

// GetNodeIDsByExecutionIDs returns the target node ids of each execution in a
// single query, keyed by execution id.
func (r *WorkflowExecutionNodeRepository) GetNodeIDsByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	result := make(map[uuid.UUID][]uuid.UUID, len(executionIDs))
	if len(executionIDs) == 0 {
		return result, nil
	}

	var rows []struct {
		ExecutionID uuid.UUID `gorm:"column:execution_id"`
		NodeID      uuid.UUID `gorm:"column:node_id"`
	}
	query := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Select("execution_id, node_id").
		Where("execution_id IN ?", executionIDs).
		Order("created_at ASC").
		Find(&rows)
	if query.Error != nil {
		return nil, query.Error
	}

	for _, row := range rows {
		result[row.ExecutionID] = append(result[row.ExecutionID], row.NodeID)
	}
	return result, nil
}

// GetIDsByExecutionIDs returns the execution-node row ids belonging to the
// given executions (retention uses them to sweep command history).
func (r *WorkflowExecutionNodeRepository) GetIDsByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(executionIDs) == 0 {
		return nil, nil
	}
	var ids []uuid.UUID
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Where("execution_id IN ?", executionIDs).
		Pluck("id", &ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return ids, nil
}

// GetActiveIDs returns the ids of all pending/running execution nodes,
// oldest first (startup re-enqueue after a restart).
func (r *WorkflowExecutionNodeRepository) GetActiveIDs(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	result := r.db.WithContext(ctx).
		Model(&WorkflowExecutionNodeModel{}).
		Where("status IN ?", []string{
			string(workflow_execution_node.StatusPending),
			string(workflow_execution_node.StatusRunning),
		}).
		Order("created_at ASC").
		Pluck("id", &ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return ids, nil
}

// DeleteByExecutionIDs deletes all execution nodes of the given executions.
func (r *WorkflowExecutionNodeRepository) DeleteByExecutionIDs(ctx context.Context, executionIDs []uuid.UUID) (int64, error) {
	if len(executionIDs) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Where("execution_id IN ?", executionIDs).
		Delete(&WorkflowExecutionNodeModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
