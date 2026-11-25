package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	"github.com/google/uuid"
)

// NodeAdoptionJobRepository persists SSH adoption runs.
type NodeAdoptionJobRepository interface {
	Create(ctx context.Context, job *node_adoption_job.Job) error
	Update(ctx context.Context, job *node_adoption_job.Job) error
	GetByID(ctx context.Context, accountID, jobID uuid.UUID) (*node_adoption_job.Job, error)
	// ResetStrandedRunning flips pending/running jobs left over from a crash to
	// failed on startup (they cannot resume — the SSH session is gone).
	ResetStrandedRunning(ctx context.Context) error
}
