package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.ExternalIdentityRepository = (*ExternalIdentityRepository)(nil)

// ExternalIdentityModel is the GORM model for OIDC identity links.
//
// RevokedAt is a SOFT delete, and the softness is the point rather than a
// convention: unlinking is an action the person it happens to cannot undo
// themselves. Once the row is gone the user cannot
// authenticate with that identity, and re-linking requires authenticating. A
// nullable column makes operator recovery a single UPDATE.
//
// It is a bare `*time.Time` and NOT `gorm.DeletedAt`: no model in this
// repository uses GORM soft deletes, so adopting one here would make this the
// only table where a plain `Find` silently changes meaning. The house idiom is
// an explicit predicate (`refresh_token_repository.go`), and every read below
// spells it out.
//
// The unique index stays on (provider, subject) WITHOUT revoked_at, so a
// revoked row still occupies the pair. That is deliberate — see Create, which
// revives it rather than inserting a second row.
type ExternalIdentityModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	Provider  string     `gorm:"column:provider;not null;uniqueIndex:uq_external_identity_provider_subject"`
	Subject   string     `gorm:"column:subject;not null;uniqueIndex:uq_external_identity_provider_subject"`
	Email     string     `gorm:"column:email"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

func (ExternalIdentityModel) TableName() string {
	return "external_identities"
}

func (m *ExternalIdentityModel) ToDomain() *external_identity.ExternalIdentity {
	return &external_identity.ExternalIdentity{
		ID:        m.ID,
		UserID:    m.UserID,
		Provider:  m.Provider,
		Subject:   m.Subject,
		Email:     m.Email,
		CreatedAt: m.CreatedAt,
	}
}

// ExternalIdentityRepository is the GORM implementation.
type ExternalIdentityRepository struct {
	db *gorm.DB
}

func NewExternalIdentityRepository(db *gorm.DB) *ExternalIdentityRepository {
	return &ExternalIdentityRepository{db: db}
}

func (r *ExternalIdentityRepository) Setup() error {
	return r.db.AutoMigrate(&ExternalIdentityModel{})
}

// Create inserts a link, or REVIVES the caller's own previously revoked one.
//
// It used to be `ON CONFLICT DO NOTHING`, which was correct only while nothing
// could revoke. With a soft delete that is a silent failure of the worst kind:
// the row still exists, so re-linking the identity you just unlinked would
// insert nothing, return no error, and tell the user it had worked while they
// remained unable to sign in with it.
//
// The `DO UPDATE ... WHERE user_id = ?` predicate does the ownership decision
// inside the statement rather than in a read-then-write, so two concurrent
// links of one IdP account cannot both believe they won. When the pair belongs
// to somebody else the predicate fails, no row is touched, and RowsAffected is
// 0 — which is the conflict, reported as one instead of passing silently.
func (r *ExternalIdentityRepository) Create(ctx context.Context, identity *external_identity.ExternalIdentity) error {
	return r.create(r.db.WithContext(ctx), identity)
}

// CreateWhileSessionLive: see the port.
func (r *ExternalIdentityRepository) CreateWhileSessionLive(ctx context.Context, identity *external_identity.ExternalIdentity, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	// The boundary itself lives in lockLiveSession (user_repository.go) and
	// is shared with the passkey and two-factor writes.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockLiveSession(tx, identity.UserID, familyID, issuedAt, generation); err != nil {
			return err
		}
		return r.create(tx, identity)
	})
}

func (r *ExternalIdentityRepository) create(db *gorm.DB, identity *external_identity.ExternalIdentity) error {
	model := &ExternalIdentityModel{
		ID:        identity.ID,
		UserID:    identity.UserID,
		Provider:  identity.Provider,
		Subject:   identity.Subject,
		Email:     identity.Email,
		CreatedAt: identity.CreatedAt,
	}
	tx := db.
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "provider"}, {Name: "subject"}},
			Where: clause.Where{Exprs: []clause.Expression{
				clause.Eq{Column: clause.Column{Table: "external_identities", Name: "user_id"}, Value: identity.UserID},
			}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"revoked_at": nil,
				"email":      identity.Email,
			}),
		}).
		Create(model)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return app_errors.ErrExternalIdentityConflict
	}
	return nil
}

// GetByProviderSubject resolves the LIVE link for an IdP account.
//
// The `revoked_at IS NULL` is what makes an unlink actually take effect: this
// is the lookup every OIDC login starts with, so without it a revoked identity
// keeps signing in and the whole feature is decoration.
func (r *ExternalIdentityRepository) GetByProviderSubject(ctx context.Context, provider, subject string) (*external_identity.ExternalIdentity, error) {
	var model ExternalIdentityModel
	err := r.db.WithContext(ctx).
		Where("provider = ? AND subject = ? AND revoked_at IS NULL", provider, subject).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrExternalIdentityNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

// RevokedExists reports whether this IdP account was DELIBERATELY unlinked from
// some user, as opposed to never having been linked.
//
// The two are indistinguishable to GetByProviderSubject by design, and the
// difference matters in exactly one place: LoginOIDC's email fallback, which
// would otherwise re-create the link the user just removed on their very next
// sign-in. See the call site.
func (r *ExternalIdentityRepository) RevokedExists(ctx context.Context, provider, subject string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&ExternalIdentityModel{}).
		Where("provider = ? AND subject = ? AND revoked_at IS NOT NULL", provider, subject).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListByUserID returns a user's LIVE links.
//
// `revoked_at IS NULL` here is not cosmetic either: this list is what the
// last-credential guard counts, so a revoked row left in it would let a user
// unlink their way down to an unreachable account one identity at a time.
func (r *ExternalIdentityRepository) ListByUserID(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error) {
	var models []ExternalIdentityModel
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	out := make([]external_identity.ExternalIdentity, 0, len(models))
	for i := range models {
		out = append(out, *models[i].ToDomain())
	}
	return out, nil
}

// Revoke soft-deletes one link, scoped to its owner.
//
// The user_id predicate is the tenancy check: an id alone would let any
// authenticated caller unlink somebody else's identity. Already-revoked rows
// are excluded so a repeated call reports not-found rather than silently
// re-stamping a newer revocation time over the original.
func (r *ExternalIdentityRepository) Revoke(ctx context.Context, userID, identityID uuid.UUID) error {
	now := time.Now()
	tx := r.db.WithContext(ctx).
		Model(&ExternalIdentityModel{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", identityID, userID).
		Update("revoked_at", now)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return app_errors.ErrExternalIdentityNotFound
	}
	return nil
}

func (r *ExternalIdentityRepository) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&ExternalIdentityModel{}).Error
}
