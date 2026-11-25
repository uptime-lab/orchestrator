package sql

import (
	"context"
	"encoding/json"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.InstanceAnomalyRepository = (*InstanceAnomalyRepository)(nil)

// InstanceAnomalyModel is the GORM model for operator-facing anomalies.
//
// This table is meant to be read by operator tooling under a least-privilege
// role, so it deliberately holds no tenant secrets — identifiers and
// measurements only.
type InstanceAnomalyModel struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Kind            string     `gorm:"column:kind;size:64;not null;index"`
	DedupeKey       string     `gorm:"column:dedupe_key;size:128;not null;uniqueIndex"`
	Severity        string     `gorm:"column:severity;size:16;not null"`
	Summary         string     `gorm:"column:summary;type:text;not null"`
	Details         []byte     `gorm:"column:details;type:jsonb"`
	Class           string     `gorm:"column:class;size:16;not null;default:'event'"`
	State           string     `gorm:"column:state;size:16;not null;default:'firing';index"`
	StateSince      time.Time  `gorm:"column:state_since;not null;default:now()"`
	AccountID       *uuid.UUID `gorm:"column:account_id;type:uuid;index"`
	OccurrenceCount int64      `gorm:"column:occurrence_count;not null;default:1"`
	FirstSeenAt     time.Time  `gorm:"column:first_seen_at;not null"`
	LastSeenAt      time.Time  `gorm:"column:last_seen_at;not null;index"`
	AcknowledgedAt  *time.Time `gorm:"column:acknowledged_at"`
	NotifiedAt      *time.Time `gorm:"column:notified_at"`
}

func (InstanceAnomalyModel) TableName() string {
	return "instance_anomalies"
}

// InstanceAnomalyRepository is the GORM implementation.
type InstanceAnomalyRepository struct {
	db *gorm.DB
}

func NewInstanceAnomalyRepository(db *gorm.DB) *InstanceAnomalyRepository {
	if db == nil {
		panic("InstanceAnomalyRepository requires a database handle")
	}
	return &InstanceAnomalyRepository{db: db}
}

func (r *InstanceAnomalyRepository) Setup() error {
	return r.db.AutoMigrate(&InstanceAnomalyModel{})
}

// Record inserts a first occurrence or folds a repeat into the existing row.
//
// The whole design rests on this being ONE statement. The condition that
// motivated the table — a device refused adoption — repeats every second or two
// for as long as the device is powered on, so a read-then-write would both race
// and, far worse, produce a table an operator cannot read.
//
// A recurrence does NOT clear AcknowledgedAt. Clearing it meant acknowledging a
// chronic problem bought one tick of silence, since these conditions re-detect
// every minute (and a refused device every second or two). Un-silencing is
// driven by a new firing CYCLE — resolve, then fire again — which the state
// transition handles, not by mere re-detection.
//
// State is likewise left alone here: this method records an occurrence, and
// deciding whether that occurrence constitutes a transition is the caller's
// job, where the "for how long" rule lives.
func (r *InstanceAnomalyRepository) Record(ctx context.Context, anomaly *instance_anomaly.Anomaly) error {
	details, err := json.Marshal(anomaly.Details)
	if err != nil {
		details = nil
	}
	model := &InstanceAnomalyModel{
		Class:           string(anomaly.Class),
		State:           string(anomaly.State),
		StateSince:      anomaly.StateSince,
		ID:              anomaly.ID,
		Kind:            string(anomaly.Kind),
		DedupeKey:       anomaly.DedupeKey,
		Severity:        string(anomaly.Severity),
		Summary:         anomaly.Summary,
		Details:         details,
		AccountID:       anomaly.AccountID,
		OccurrenceCount: 1,
		FirstSeenAt:     anomaly.FirstSeenAt,
		LastSeenAt:      anomaly.LastSeenAt,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "dedupe_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"occurrence_count": gorm.Expr("instance_anomalies.occurrence_count + 1"),
			"last_seen_at":     model.LastSeenAt,
			"severity":         model.Severity,
			"summary":          model.Summary,
			"details":          model.Details,
			// A re-detected condition is firing again. Without this a condition
			// that resolved and came back would keep its resolved state while
			// its occurrence count climbed — visible only to whoever thought to
			// compare the two. State moves; state_since only moves ON the
			// transition, so "firing for 20 minutes" stays answerable.
			"state": instance_anomaly.StateFiring,
			"state_since": gorm.Expr(
				"CASE WHEN instance_anomalies.state <> ? THEN ? ELSE instance_anomalies.state_since END",
				string(instance_anomaly.StateFiring), model.StateSince),
		}),
	}).Create(model).Error
}

// ResolveCondition marks a firing condition resolved.
//
// Only conditions: an event describes something that happened and cannot stop
// having happened. The guard on the current state is what keeps state_since
// meaningful — re-resolving an already-resolved row would keep pushing the
// timestamp forward and make a long-settled condition look like it just
// cleared.
func (r *InstanceAnomalyRepository) ResolveCondition(ctx context.Context, dedupeKey string) error {
	return r.db.WithContext(ctx).
		Model(&InstanceAnomalyModel{}).
		Where("dedupe_key = ? AND class = ? AND state <> ?",
			dedupeKey, string(instance_anomaly.ClassCondition), string(instance_anomaly.StateResolved)).
		Updates(map[string]interface{}{
			"state":       string(instance_anomaly.StateResolved),
			"state_since": time.Now(),
		}).Error
}

// ListSerialBindingRefusals projects the requesting account's own refusals.
//
// Projection, never a model scan: hydrating InstanceAnomalyModel would carry
// details.owner_account_id and owner_node_id into the tenant's request, and the
// safest place to drop another tenant's identifiers is inside the SELECT list.
// The serial is read out of the jsonb rather than the hashed dedupe key, which
// exists precisely so the serial is not in an index the whole operator surface
// reads.
func (r *InstanceAnomalyRepository) ListSerialBindingRefusals(ctx context.Context, accountID uuid.UUID, limit int) ([]repository.SerialBindingRefusal, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []struct {
		HardwareSerial  string
		FirstSeenAt     time.Time
		LastSeenAt      time.Time
		OccurrenceCount int64
	}
	result := r.db.WithContext(ctx).
		Table("instance_anomalies").
		Select("details->>'hardware_serial' AS hardware_serial, first_seen_at, last_seen_at, occurrence_count").
		Where("account_id = ? AND kind = ? AND details->>'hardware_serial' IS NOT NULL",
			accountID, string(instance_anomaly.KindSerialBindingConflict)).
		Order("last_seen_at DESC").
		Limit(limit).
		Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}
	out := make([]repository.SerialBindingRefusal, 0, len(rows))
	for _, row := range rows {
		out = append(out, repository.SerialBindingRefusal{
			HardwareSerial:  row.HardwareSerial,
			FirstSeenAt:     row.FirstSeenAt,
			LastSeenAt:      row.LastSeenAt,
			OccurrenceCount: row.OccurrenceCount,
		})
	}
	return out, nil
}
