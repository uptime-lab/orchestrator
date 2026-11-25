package sql

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"gorm.io/gorm"
)

var (
	_ repository.AuditEventRepository     = (*AuditEventRepository)(nil)
	_ repository.AuditRetentionRepository = (*AuditEventRepository)(nil)
)

// AuditEventModel is the GORM model for the append-only audit log.
type AuditEventModel struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	AccountID   uuid.UUID  `gorm:"column:account_id;type:uuid;not null;index"`
	ActorUserID *uuid.UUID `gorm:"column:actor_user_id;type:uuid"`
	ActorKind   string     `gorm:"column:actor_kind;not null"`
	Action      string     `gorm:"column:action;not null"`
	TargetType  string     `gorm:"column:target_type;not null"`
	TargetID    string     `gorm:"column:target_id;not null"`
	// Detail is nullable-by-default (no `not null`) so AutoMigrate can add it to
	// a table that already has rows. Never carries credential material — see
	// audit_event.AuditEvent.Detail.
	Detail    string    `gorm:"column:detail"`
	CreatedAt time.Time `gorm:"column:created_at;not null;index"`
}

func (AuditEventModel) TableName() string {
	return "audit_events"
}

// AuditEventRepository is the GORM implementation of the audit log.
type AuditEventRepository struct {
	db *gorm.DB
}

func NewAuditEventRepository(db *gorm.DB) *AuditEventRepository {
	return &AuditEventRepository{db: db}
}

func (r *AuditEventRepository) Setup() error {
	return r.db.AutoMigrate(&AuditEventModel{})
}

func (r *AuditEventRepository) Create(ctx context.Context, event *audit_event.AuditEvent) error {
	model := &AuditEventModel{
		ID:          event.ID,
		AccountID:   event.AccountID,
		ActorUserID: event.ActorUserID,
		ActorKind:   string(event.ActorKind),
		Action:      event.Action,
		TargetType:  event.TargetType,
		TargetID:    event.TargetID,
		Detail:      event.Detail,
		CreatedAt:   event.CreatedAt,
	}
	return r.db.WithContext(ctx).Create(model).Error
}

// List returns a page of an account's events, newest first, plus the total
// matching the filter. The count and the page share one filtered query base so
// they cannot disagree about what "matching" means.
func (r *AuditEventRepository) List(ctx context.Context, accountID uuid.UUID, filter repository.AuditEventListFilter, offset, limit int) ([]audit_event.AuditEvent, int64, error) {
	base := r.db.WithContext(ctx).Model(&AuditEventModel{}).Where("account_id = ?", accountID)
	if filter.Action != "" {
		base = base.Where("action = ?", filter.Action)
	}
	if filter.ActorUserID != nil {
		base = base.Where("actor_user_id = ?", *filter.ActorUserID)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var models []AuditEventModel
	err := base.Session(&gorm.Session{}).
		Order("created_at DESC").
		Offset(offset).Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, 0, err
	}
	events := make([]audit_event.AuditEvent, 0, len(models))
	for i := range models {
		events = append(events, audit_event.AuditEvent{
			ID:          models[i].ID,
			AccountID:   models[i].AccountID,
			ActorUserID: models[i].ActorUserID,
			ActorKind:   audit_event.ActorKind(models[i].ActorKind),
			Action:      models[i].Action,
			TargetType:  models[i].TargetType,
			TargetID:    models[i].TargetID,
			Detail:      models[i].Detail,
			CreatedAt:   models[i].CreatedAt,
		})
	}
	return events, total, nil
}

// CountByAccountAndActionsSince counts an account's events of the given actions
// within a window, so a rate limit can be counted off the append-only trail
// instead of off the table it bounds.
//
// Indexed on account_id (and separately on created_at), and the caller is an
// endpoint a human clicks — this is not a hot path, and it must not become one:
// counting on every request of something frequent would want its own counter.
func (r *AuditEventRepository) CountByAccountAndActionsSince(ctx context.Context, accountID uuid.UUID, actions []string, since time.Time) (int64, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&AuditEventModel{}).
		Where("account_id = ? AND action IN ? AND created_at >= ?", accountID, actions, since).
		Count(&count).Error
	return count, err
}

// The retention sweep's statements: the only deletion path for this table,
// reachable from no request handler — retention runs on a timer, so nothing a
// user or an operator does through the API can erase the record of what they
// did. Each deletes at most limit rows of ONE account, chosen by
// `account_id = ? AND created_at < ?`, so one call is one short statement
// whose cost is bounded by that account's rows.

// ListAuditRetentionOverrides reads the accounts that carry their own value.
func (r *AuditEventRepository) ListAuditRetentionOverrides(ctx context.Context) ([]repository.AuditRetentionOverride, error) {
	var rows []struct {
		ID                 uuid.UUID
		AuditRetentionDays int
	}
	err := r.db.WithContext(ctx).Model(&AccountModel{}).
		Select("id, audit_retention_days").
		Where("audit_retention_days IS NOT NULL").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]repository.AuditRetentionOverride, 0, len(rows))
	for _, row := range rows {
		out = append(out, repository.AuditRetentionOverride{AccountID: row.ID, Days: row.AuditRetentionDays})
	}
	return out, nil
}

// ListInstallDefaultAuditAccounts returns the accounts the install default
// governs: every account without its own value, and every account id the log
// still carries whose account no longer exists.
//
// The sweep is driven per account so each delete is bounded by the rows it can
// actually remove. One statement over `created_at < cutoff AND account_id NOT
// IN (overridden)` read every old row of every account kept LONGER than the
// default — forever, for an account that keeps its history — on every tick,
// only to discard it, because NOT IN cannot use the account_id index and the
// LIMIT never ends the scan early once the deletable rows are gone.
//
// The orphaned ids are enumerated with a loose index scan over the account_id
// index (one probe per distinct id, never a pass over the rows), so this read
// costs the number of accounts in the log, not its size.
func (r *AuditEventRepository) ListInstallDefaultAuditAccounts(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := r.db.WithContext(ctx).Model(&AccountModel{}).
		Where("audit_retention_days IS NULL").
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	var orphaned []struct{ AccountID uuid.UUID }
	if err := r.db.WithContext(ctx).Raw(`
WITH RECURSIVE ids AS (
	(SELECT account_id FROM audit_events ORDER BY account_id LIMIT 1)
	UNION ALL
	SELECT (SELECT e.account_id FROM audit_events e
	        WHERE e.account_id > ids.account_id ORDER BY e.account_id LIMIT 1)
	FROM ids WHERE ids.account_id IS NOT NULL
)
SELECT account_id FROM ids
WHERE account_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.id = ids.account_id)`).
		Scan(&orphaned).Error; err != nil {
		return nil, err
	}
	for _, row := range orphaned {
		ids = append(ids, row.AccountID)
	}
	return ids, nil
}

// DeleteAccountOlderThanAtInstallDefault deletes one account's events older
// than cutoff while the install default still governs it: the account carries
// no value of its own, or no longer exists. The guard is read in the same
// statement, so an account given its own value since it was listed is never
// swept at the install default.
func (r *AuditEventRepository) DeleteAccountOlderThanAtInstallDefault(ctx context.Context, accountID uuid.UUID, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	overridden := r.db.Model(&AccountModel{}).Select("1").
		Where("id = ? AND audit_retention_days IS NOT NULL", accountID)
	result := r.db.WithContext(ctx).
		Where("id IN (?)",
			r.db.Model(&AuditEventModel{}).
				Select("id").
				Where("account_id = ? AND created_at < ?", accountID, cutoff).
				Where("NOT EXISTS (?)", overridden).
				Limit(limit)).
		Delete(&AuditEventModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// DeleteAccountOlderThan deletes one account's events older than cutoff,
// while the account's stored value is still the one the cutoff came from.
func (r *AuditEventRepository) DeleteAccountOlderThan(ctx context.Context, override repository.AuditRetentionOverride, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	stillSet := r.db.Model(&AccountModel{}).Select("1").
		Where("id = ? AND audit_retention_days = ?", override.AccountID, override.Days)
	result := r.db.WithContext(ctx).
		Where("id IN (?)",
			r.db.Model(&AuditEventModel{}).
				Select("id").
				Where("account_id = ? AND created_at < ?", override.AccountID, cutoff).
				Where("EXISTS (?)", stillSet).
				Limit(limit)).
		Delete(&AuditEventModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
