package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// InstanceEntitlementRepository stores the entitlement files applied to this
// install. Append-only: a file is never edited or deleted through the API,
// because the newest valid one wins at read time and the history is what
// shows an operator which file is in force.
type InstanceEntitlementRepository interface {
	// Create appends an applied file.
	Create(ctx context.Context, rec *entitlement.Record) error
	// ListNewest returns up to limit records, newest issued first. The
	// resolver re-verifies each one; a bound keeps a read from growing with
	// every paste ever made.
	ListNewest(ctx context.Context, limit int) ([]entitlement.Record, error)
}
