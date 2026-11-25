package sql

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

var _ repository.InstanceEntitlementRepository = (*InstanceEntitlementRepository)(nil)

// InstanceEntitlementModel is one applied entitlement file. No account_id:
// the entitlement is a property of the install, so the row belongs to no
// tenant and no tenant purge may reach it.
type InstanceEntitlementModel struct {
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	// Document is the paste form verbatim. Re-verified on every read against
	// the keys the running build trusts, so a key rotation that drops a key
	// is visible as a rejected row rather than as a silently trusted one.
	Document           string    `gorm:"column:document;type:text;not null"`
	CustomerID         string    `gorm:"column:customer_id;size:128;not null"`
	Kind               string    `gorm:"column:kind;size:32;not null"`
	DeviceCap          int       `gorm:"column:device_cap;not null"`
	IssuedAt           time.Time `gorm:"column:issued_at;not null;index"`
	ExpiresAt          time.Time `gorm:"column:expires_at;not null"`
	AppliedAt          time.Time `gorm:"column:applied_at;not null;default:now()"`
	AppliedByUserID    uuid.UUID `gorm:"column:applied_by_user_id;type:uuid;not null"`
	AppliedByAccountID uuid.UUID `gorm:"column:applied_by_account_id;type:uuid;not null"`
}

func (InstanceEntitlementModel) TableName() string { return "instance_entitlement" }

func (m *InstanceEntitlementModel) ToDomain() entitlement.Record {
	return entitlement.Record{
		ID:                 m.ID,
		Document:           m.Document,
		CustomerID:         m.CustomerID,
		Kind:               entitlement.Kind(m.Kind),
		DeviceCap:          m.DeviceCap,
		IssuedAt:           m.IssuedAt,
		ExpiresAt:          m.ExpiresAt,
		AppliedAt:          m.AppliedAt,
		AppliedByUserID:    m.AppliedByUserID,
		AppliedByAccountID: m.AppliedByAccountID,
	}
}

func fromDomainInstanceEntitlement(r *entitlement.Record) *InstanceEntitlementModel {
	return &InstanceEntitlementModel{
		ID:                 r.ID,
		Document:           r.Document,
		CustomerID:         r.CustomerID,
		Kind:               string(r.Kind),
		DeviceCap:          r.DeviceCap,
		IssuedAt:           r.IssuedAt,
		ExpiresAt:          r.ExpiresAt,
		AppliedAt:          r.AppliedAt,
		AppliedByUserID:    r.AppliedByUserID,
		AppliedByAccountID: r.AppliedByAccountID,
	}
}

type InstanceEntitlementRepository struct {
	db *gorm.DB
}

func NewInstanceEntitlementRepository(db *gorm.DB) *InstanceEntitlementRepository {
	return &InstanceEntitlementRepository{db: db}
}

func (r *InstanceEntitlementRepository) Setup() error {
	return r.db.AutoMigrate(&InstanceEntitlementModel{})
}

func (r *InstanceEntitlementRepository) Create(ctx context.Context, rec *entitlement.Record) error {
	return r.db.WithContext(ctx).Create(fromDomainInstanceEntitlement(rec)).Error
}

func (r *InstanceEntitlementRepository) ListNewest(ctx context.Context, limit int) ([]entitlement.Record, error) {
	if limit <= 0 {
		limit = 1
	}
	var models []InstanceEntitlementModel
	if err := r.db.WithContext(ctx).
		Order("issued_at DESC, applied_at DESC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]entitlement.Record, 0, len(models))
	for i := range models {
		out = append(out, models[i].ToDomain())
	}
	return out, nil
}
