package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_deletion_request"
)

// AccountDeletionRequestRepository stores a tenant's standing request to be
// erased. At most one is open per account, enforced by a unique index rather
// than by a read-then-write in the service.
type AccountDeletionRequestRepository interface {
	// Create inserts the request, returning
	// account_deletion_request.ErrAlreadyRequested when one is already open.
	Create(ctx context.Context, req *account_deletion_request.Request) error
	// GetByAccountID returns the open request, or app_errors.ErrNotFound.
	GetByAccountID(ctx context.Context, accountID uuid.UUID) (*account_deletion_request.Request, error)
	// DeleteByAccountID cancels the request and reports whether one existed.
	DeleteByAccountID(ctx context.Context, accountID uuid.UUID) (bool, error)
}
