package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.NodeFavoriteRepository = (*NodeFavoriteRepository)(nil)

// NodeFavoriteModel is one user's favorite mark on one node.
type NodeFavoriteModel struct {
	UserID    uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey;not null"`
	NodeID    uuid.UUID `gorm:"column:node_id;type:uuid;primaryKey;not null;index"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (NodeFavoriteModel) TableName() string {
	return "user_favorite_nodes"
}

type NodeFavoriteRepository struct {
	db *gorm.DB
}

func NewNodeFavoriteRepository(db *gorm.DB) *NodeFavoriteRepository {
	return &NodeFavoriteRepository{db: db}
}

func (r *NodeFavoriteRepository) Setup() error {
	return r.db.AutoMigrate(&NodeFavoriteModel{})
}

func (r *NodeFavoriteRepository) ListNodeIDsByUser(ctx context.Context, accountID, userID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	// Join nodes to keep the answer inside the asking account: marks are keyed
	// (user, node) alone, and a multi-account user must not see one account's
	// node UUIDs while working in another. The join also drops marks whose node
	// has since been deleted.
	err := r.db.WithContext(ctx).
		Model(&NodeFavoriteModel{}).
		Joins("JOIN nodes ON nodes.id = user_favorite_nodes.node_id AND nodes.account_id = ?", accountID).
		Where("user_favorite_nodes.user_id = ?", userID).
		Pluck("user_favorite_nodes.node_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *NodeFavoriteRepository) Set(ctx context.Context, userID, nodeID uuid.UUID, favorite bool) error {
	if !favorite {
		return r.db.WithContext(ctx).
			Where("user_id = ? AND node_id = ?", userID, nodeID).
			Delete(&NodeFavoriteModel{}).Error
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&NodeFavoriteModel{UserID: userID, NodeID: nodeID, CreatedAt: time.Now()}).Error
}

func (r *NodeFavoriteRepository) DeleteByNode(ctx context.Context, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("node_id = ?", nodeID).
		Delete(&NodeFavoriteModel{}).Error
}
