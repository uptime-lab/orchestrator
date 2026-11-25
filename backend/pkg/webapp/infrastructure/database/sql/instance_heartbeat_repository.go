package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InstanceHeartbeatModel is one row per long-running component of the control
// plane, holding when it last ticked and when the process it belongs to started.
//
// It exists for operator tooling that reads the database rather than the API
// and so has no way to ask the backend anything. Two questions such a reader
// could not answer without this table:
//
//   - "Is the backend restarting in a loop?" A container coming back every
//     ninety seconds looks perfectly healthy to every other detector — nodes
//     reconnect, the disk is fine, the certificate is valid — and the only
//     visible symptom is that nothing quite works. StartedAt changing between
//     samples is the signal.
//   - "Is the workflow scheduler still ticking?" A wedged scheduler produces no
//     error and no missing row; cron playbooks simply stop firing, and the
//     absence of a run is indistinguishable from a quiet week. Inferring it from
//     the playbooks' cron expressions would mean parsing them a second time, in
//     a second module, and still being wrong for a fleet that legitimately has
//     nothing scheduled today.
//
// One row per component, upserted. This is a liveness table, not a history: it
// must not grow, and what happened last Tuesday is what the anomaly rows are for.
type InstanceHeartbeatModel struct {
	Component   string    `gorm:"column:component;size:64;primaryKey"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at;not null"`
	StartedAt   time.Time `gorm:"column:started_at;not null"`
	Version     string    `gorm:"column:version;size:64"`
	DetailCount int64     `gorm:"column:detail_count;not null;default:0"`
}

func (InstanceHeartbeatModel) TableName() string {
	return "instance_heartbeats"
}

var _ repository.InstanceHeartbeatRepository = (*InstanceHeartbeatRepository)(nil)

// InstanceHeartbeatRepository writes the liveness rows.
type InstanceHeartbeatRepository struct {
	db *gorm.DB
}

func NewInstanceHeartbeatRepository(db *gorm.DB) *InstanceHeartbeatRepository {
	if db == nil {
		panic("InstanceHeartbeatRepository requires a database handle")
	}
	return &InstanceHeartbeatRepository{db: db}
}

func (r *InstanceHeartbeatRepository) Setup() error {
	return r.db.AutoMigrate(&InstanceHeartbeatModel{})
}

// Beat records that a component is alive now.
//
// startedAt is the PROCESS start, not this tick — it is the field the restart
// detector compares between samples, so it must stay constant for the life of
// the process. detailCount is a component-specific number the console can show
// without a second query (the scheduler reports how many playbooks it evaluated).
func (r *InstanceHeartbeatRepository) Beat(ctx context.Context, component string, startedAt time.Time, version string, detailCount int64) error {
	model := &InstanceHeartbeatModel{
		Component:   component,
		LastSeenAt:  time.Now(),
		StartedAt:   startedAt,
		Version:     version,
		DetailCount: detailCount,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "component"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_seen_at", "started_at", "version", "detail_count",
		}),
	}).Create(model).Error
}
