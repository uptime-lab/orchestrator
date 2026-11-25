package services

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
)

// MasterTokenService defines the interface for master token operations
type MasterTokenService interface {
	// ValidateToken validates a master token and returns the token if valid
	ValidateToken(ctx context.Context, tokenValue string) (*master_token.MasterToken, error)
}
