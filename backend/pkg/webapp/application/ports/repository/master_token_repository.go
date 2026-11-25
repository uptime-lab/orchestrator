package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
	"github.com/google/uuid"
)

type MasterTokenRepository interface {
	Create(ctx context.Context, token *master_token.MasterToken) error
	Update(ctx context.Context, token *master_token.MasterToken) error
	GetByID(ctx context.Context, accountID, tokenID uuid.UUID) (*master_token.MasterToken, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (*master_token.MasterToken, error)
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]master_token.MasterToken, error)
	Delete(ctx context.Context, accountID, tokenID uuid.UUID) error
	// DeleteByOUID revokes every token scoped to an organizational unit (OU
	// deletion cleanup) — an adoption credential pointing at a dead OU must
	// not stay usable.
	DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error
	UpdateLastUsed(ctx context.Context, tokenID uuid.UUID) error
}
