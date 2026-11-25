package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/google/uuid"
)

// OrganizationalUnitRepository persists organizational units.
type OrganizationalUnitRepository interface {
	Create(ctx context.Context, ou *organizational_unit.OrganizationalUnit) error
	Update(ctx context.Context, ou *organizational_unit.OrganizationalUnit) error
	GetByID(ctx context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error)
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]organizational_unit.OrganizationalUnit, error)
	Delete(ctx context.Context, accountID, id uuid.UUID) error
	// CountChildren returns how many units are nested under parentID.
	CountChildren(ctx context.Context, accountID, parentID uuid.UUID) (int64, error)
}

// AccountPolicyRepository persists the account-wide policy fallback (at most
// one record per account).
type AccountPolicyRepository interface {
	// Get returns the account policy, or nil when the account never set one.
	Get(ctx context.Context, accountID uuid.UUID) (*organizational_unit.AccountPolicy, error)
	// Upsert creates or replaces the account policy.
	Upsert(ctx context.Context, policy *organizational_unit.AccountPolicy) error
}
