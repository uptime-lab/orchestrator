package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/adoption_code"
	"github.com/google/uuid"
)

// AdoptionCodeRepository stores one-time adoption-link exchange codes.
type AdoptionCodeRepository interface {
	// Create stores a freshly minted code (hash only).
	Create(ctx context.Context, code *adoption_code.AdoptionCode) error

	// ClaimByHash atomically consumes an unused, unexpired code and returns
	// it. A second claim of the same code — or a claim of an expired or
	// unknown one — fails identically (no oracle).
	ClaimByHash(ctx context.Context, codeHash string) (*adoption_code.AdoptionCode, error)

	// DeleteByNodeID revokes all codes minted for a node (re-mint, node delete).
	DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error

	// DeleteExpired sweeps stale rows (called opportunistically on mint).
	DeleteExpired(ctx context.Context) error
}
