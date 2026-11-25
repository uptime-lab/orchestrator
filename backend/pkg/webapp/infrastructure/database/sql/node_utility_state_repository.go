package sql

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

var _ repository.NodeUtilityStateRepository = (*NodeUtilityStateRepository)(nil)

// NodeUtilityStateRepository persists the per-(node, utility) ladder rung.
type NodeUtilityStateRepository struct {
	db *gorm.DB
}

func NewNodeUtilityStateRepository(db *gorm.DB) *NodeUtilityStateRepository {
	return &NodeUtilityStateRepository{db: db}
}

func (r *NodeUtilityStateRepository) Setup() error {
	return r.db.AutoMigrate(&NodeUtilityStateModel{})
}

// SetDesired upserts the resolved desired hash. The (node_id, utility_id)
// unique index makes the upsert race-safe.
func (r *NodeUtilityStateRepository) SetDesired(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, desiredHash, desiredSourceHash string) error {
	model := &NodeUtilityStateModel{
		ID:                uuid.New(),
		AccountID:         accountID,
		NodeID:            nodeID,
		UtilityID:         utilityID,
		DesiredHash:       desiredHash,
		DesiredSourceHash: desiredSourceHash,
		UpdatedAt:         time.Now(),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}, {Name: "utility_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"desired_hash":        desiredHash,
			"desired_source_hash": desiredSourceHash,
			// A config CHANGE resets the stand-down streak: the failures no
			// longer describe the config about to be applied. Same-hash
			// re-applies keep the streak, or the cap could be defeated by the
			// reconcile loop's own retries.
			"drift_failures": gorm.Expr(
				"CASE WHEN node_utility_states.desired_source_hash <> ? THEN 0 ELSE node_utility_states.drift_failures END",
				desiredSourceHash),
			"updated_at": time.Now(),
		}),
	}).Create(model).Error
}

// RecordApplied is called ONLY from the settle path, with the hash from the
// platform's own dispatch payload.
func (r *NodeUtilityStateRepository) RecordApplied(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, appliedHash string) error {
	model := &NodeUtilityStateModel{
		ID:          uuid.New(),
		AccountID:   accountID,
		NodeID:      nodeID,
		UtilityID:   utilityID,
		DesiredHash: appliedHash,
		AppliedHash: appliedHash,
		UpdatedAt:   time.Now(),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}, {Name: "utility_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"applied_hash":   appliedHash,
			"last_error":     "",
			"drift_failures": 0,
			"updated_at":     time.Now(),
		}),
	}).Create(model).Error
}

// RecordFailed clears the applied hash — a failed apply must read as
// not-converged, never as the hash it failed to reach.
func (r *NodeUtilityStateRepository) RecordFailed(ctx context.Context, accountID, nodeID uuid.UUID, utilityID, message string) error {
	model := &NodeUtilityStateModel{
		ID:        uuid.New(),
		AccountID: accountID,
		NodeID:    nodeID,
		UtilityID: utilityID,
		LastError: message,
		UpdatedAt: time.Now(),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}, {Name: "utility_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"applied_hash":   "",
			"last_error":     message,
			"drift_failures": gorm.Expr("node_utility_states.drift_failures + 1"),
			"updated_at":     time.Now(),
		}),
	}).Create(model).Error
}

func (r *NodeUtilityStateRepository) ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]utility.NodeState, error) {
	var models []NodeUtilityStateModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND node_id = ?", accountID, nodeID).
		Order("utility_id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]utility.NodeState, 0, len(models))
	for i := range models {
		out = append(out, models[i].toDomain())
	}
	return out, nil
}

// ListByNodes reads the ladder rows for a set of nodes in one query. Batched by
// the caller's node list rather than keyset-paged: a unit's node set is bounded
// by NODE_MAX_PER_ACCOUNT, and the rows are (node, utility) pairs over a
// single-digit catalog.
func (r *NodeUtilityStateRepository) ListByNodes(ctx context.Context, accountID uuid.UUID, nodeIDs []uuid.UUID) ([]utility.NodeState, error) {
	if len(nodeIDs) == 0 {
		// `node_id IN ()` is not valid SQL and GORM renders it as a match-nothing
		// clause on some dialects and an error on others; answering here keeps
		// the empty-unit case off that difference entirely.
		return nil, nil
	}
	var models []NodeUtilityStateModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND node_id IN ?", accountID, nodeIDs).
		Order("node_id ASC, utility_id ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]utility.NodeState, 0, len(models))
	for i := range models {
		out = append(out, models[i].toDomain())
	}
	return out, nil
}

func (r *NodeUtilityStateRepository) DeleteMissing(ctx context.Context, accountID, nodeID uuid.UUID, keep []string) error {
	q := r.db.WithContext(ctx).
		Where("account_id = ? AND node_id = ?", accountID, nodeID)
	if len(keep) > 0 {
		q = q.Where("utility_id NOT IN ?", keep)
	}
	return q.Delete(&NodeUtilityStateModel{}).Error
}

func (r *NodeUtilityStateRepository) DeleteByNode(ctx context.Context, accountID, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND node_id = ?", accountID, nodeID).
		Delete(&NodeUtilityStateModel{}).Error
}
