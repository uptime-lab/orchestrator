package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
)

// SerialBindingRefusal is the tenant-visible projection of one
// serial_binding_conflict row: the serial the tenant's own device presented and
// when. Nothing about the OWNING account crosses this boundary — the row's
// details carry owner_account_id and owner_node_id, and those are exactly the
// fields a projection exists to leave behind.
type SerialBindingRefusal struct {
	HardwareSerial  string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	OccurrenceCount int64
}

// InstanceAnomalyRepository persists operator-facing anomalies.
type InstanceAnomalyRepository interface {
	// Record inserts a first occurrence or folds a repeat into the existing
	// row, keyed on the anomaly's DedupeKey. Must be a single statement: the
	// conditions this table exists for repeat every few seconds.
	Record(ctx context.Context, anomaly *instance_anomaly.Anomaly) error
	// ResolveCondition clears a firing CONDITION (never an event, which cannot
	// stop having happened). A condition is defined by a predicate holding, and
	// something has to observe that it stopped — otherwise the row fires
	// forever and the operator learns to ignore the kind.
	ResolveCondition(ctx context.Context, dedupeKey string) error
	// ListSerialBindingRefusals returns the serial_binding_conflict rows whose
	// REQUESTING account is the given one, newest activity first, bounded.
	//
	// This is the one tenant-facing read of this table, and it is a
	// projection on purpose: the table is read by an operator console outside
	// this tree, its column list is a contract with it, and a row's details
	// name another tenant. The projection adds no column and returns no owner
	// field.
	ListSerialBindingRefusals(ctx context.Context, accountID uuid.UUID, limit int) ([]SerialBindingRefusal, error)
}
