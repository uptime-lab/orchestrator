package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.NodeAdoptionJobRepository = (*NodeAdoptionJobRepository)(nil)

// NodeAdoptionJobRepository stores SSH adoption runs.
type NodeAdoptionJobRepository struct {
	db *gorm.DB
}

// NewNodeAdoptionJobRepository creates the repository.
func NewNodeAdoptionJobRepository(db *gorm.DB) *NodeAdoptionJobRepository {
	return &NodeAdoptionJobRepository{db: db}
}

// Setup migrates the table.
func (r *NodeAdoptionJobRepository) Setup() error {
	return r.db.AutoMigrate(&NodeAdoptionJobModel{})
}

func (r *NodeAdoptionJobRepository) Create(ctx context.Context, job *node_adoption_job.Job) error {
	model := &NodeAdoptionJobModel{}
	model.FromDomain(job)
	return r.db.WithContext(ctx).Create(model).Error
}

func (r *NodeAdoptionJobRepository) Update(ctx context.Context, job *node_adoption_job.Job) error {
	model := &NodeAdoptionJobModel{}
	model.FromDomain(job)
	model.UpdatedAt = time.Now()
	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", job.ID, job.AccountID), model)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeAdoptionJobNotFound
	}
	return nil
}

func (r *NodeAdoptionJobRepository) GetByID(ctx context.Context, accountID, jobID uuid.UUID) (*node_adoption_job.Job, error) {
	var model NodeAdoptionJobModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", jobID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeAdoptionJobNotFound
		}
		return nil, result.Error
	}
	return model.ToDomain(), nil
}

// ResetStrandedRunning flips pending/running jobs to failed on startup.
func (r *NodeAdoptionJobRepository) ResetStrandedRunning(ctx context.Context) error {
	if !r.db.Migrator().HasTable(&NodeAdoptionJobModel{}) {
		return nil
	}
	return r.db.WithContext(ctx).
		Model(&NodeAdoptionJobModel{}).
		Where("status IN ?", []string{
			string(node_adoption_job.StatusPending),
			string(node_adoption_job.StatusRunning),
		}).
		Updates(map[string]interface{}{
			"status":     string(node_adoption_job.StatusFailed),
			"error":      "interrupted by a server restart",
			"updated_at": time.Now(),
		}).Error
}
