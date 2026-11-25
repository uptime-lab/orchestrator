package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.TwoFactorRepository = (*TwoFactorRepository)(nil)

// TwoFactorModel is the GORM model for a user's second factor.
type TwoFactorModel struct {
	UserID uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	// Secret holds the ENCRYPTED TOTP secret. Never the raw value: it is a
	// password-equivalent credential, so a database read must not yield a
	// working factor.
	Secret string `gorm:"column:secret;type:text;not null"`
	// RecoveryCodes are SHA-256 digests, not the codes themselves.
	RecoveryCodes textArray `gorm:"column:recovery_codes;type:text[]"`
	// LastUsedStep is the TOTP time step (unix/period) most recently accepted,
	// the RFC 6238 anti-replay ledger.
	//
	// NULLABLE, and that is load-bearing: a NOT NULL column with no default
	// succeeds on an empty table and is fatal where rows exist. It also has to
	// tolerate NULL in the predicate — a
	// bare `<` against NULL matches nothing, which would refuse every code
	// forever for a factor that predates this column.
	LastUsedStep *int64 `gorm:"column:last_used_step"`
	ConfirmedAt   *time.Time `gorm:"column:confirmed_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;not null"`
}

func (TwoFactorModel) TableName() string {
	return "two_factors"
}

func (m *TwoFactorModel) ToDomain() *two_factor.TwoFactor {
	return &two_factor.TwoFactor{
		UserID:             m.UserID,
		SecretCiphertext:   m.Secret,
		RecoveryCodeHashes: []string(m.RecoveryCodes),
		ConfirmedAt:        m.ConfirmedAt,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
}

func twoFactorFromDomain(f *two_factor.TwoFactor) *TwoFactorModel {
	return &TwoFactorModel{
		UserID:        f.UserID,
		Secret:        f.SecretCiphertext,
		RecoveryCodes: textArray(f.RecoveryCodeHashes),
		ConfirmedAt:   f.ConfirmedAt,
		CreatedAt:     f.CreatedAt,
		UpdatedAt:     f.UpdatedAt,
	}
}

// TwoFactorRepository is the GORM implementation.
type TwoFactorRepository struct {
	db *gorm.DB
}

func NewTwoFactorRepository(db *gorm.DB) *TwoFactorRepository {
	if db == nil {
		panic("TwoFactorRepository requires a database handle")
	}
	return &TwoFactorRepository{db: db}
}

func (r *TwoFactorRepository) Setup() error {
	return r.db.AutoMigrate(&TwoFactorModel{})
}

func (r *TwoFactorRepository) GetByUserID(ctx context.Context, userID uuid.UUID) (*two_factor.TwoFactor, error) {
	var model TwoFactorModel
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrTwoFactorNotEnrolled
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

// Upsert replaces the row wholesale. Re-enrolling therefore discards a previous
// PENDING secret, which is what "start over" should mean; a CONFIRMED factor is
// protected by the service, which refuses to overwrite one without disabling it
// first.
func (r *TwoFactorRepository) Upsert(ctx context.Context, factor *two_factor.TwoFactor) error {
	model := twoFactorFromDomain(factor)
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		UpdateAll: true,
	}).Create(model).Error
}

// ConsumeRecoveryCode removes one hash from the array in a single statement.
//
// array_remove inside the UPDATE, guarded by an = ANY() predicate, so the
// database decides the winner: RowsAffected is 1 for exactly one of two
// concurrent attempts with the same code.
// CreatePending: see the port. The ON CONFLICT ... WHERE keeps an enabled row
// out of reach of the upsert, so the refusal and the write are one statement.
func (r *TwoFactorRepository) CreatePending(ctx context.Context, factor *two_factor.TwoFactor) error {
	model := twoFactorFromDomain(factor)
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "two_factors.confirmed_at IS NULL"}}},
		DoUpdates: clause.AssignmentColumns([]string{"secret", "recovery_codes", "last_used_step", "confirmed_at", "created_at", "updated_at"}),
	}).Create(model)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrTwoFactorAlreadyEnabled
	}
	return nil
}

// ConfirmPending: see the port.
func (r *TwoFactorRepository) ConfirmPending(ctx context.Context, factor *two_factor.TwoFactor, expectedSecret string) error {
	return r.confirmPending(r.db.WithContext(ctx), factor, expectedSecret)
}

// ConfirmPendingWhileSessionLive: see the port. Enabling a factor is a
// durable credential write, so it shares lockLiveSession
// (user_repository.go) with the OIDC identity and the passkey
// the statement inside is ConfirmPending's, unchanged.
func (r *TwoFactorRepository) ConfirmPendingWhileSessionLive(ctx context.Context, factor *two_factor.TwoFactor, expectedSecret string, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockLiveSession(tx, factor.UserID, familyID, issuedAt, generation); err != nil {
			return err
		}
		return r.confirmPending(tx, factor, expectedSecret)
	})
}

// confirmPending is the ONE conditional statement both confirm paths run.
func (r *TwoFactorRepository) confirmPending(db *gorm.DB, factor *two_factor.TwoFactor, expectedSecret string) error {
	model := twoFactorFromDomain(factor)
	res := db.Model(&TwoFactorModel{}).
		Where("user_id = ? AND confirmed_at IS NULL AND secret = ?", factor.UserID, expectedSecret).
		Updates(map[string]interface{}{
			"confirmed_at":   model.ConfirmedAt,
			"recovery_codes": model.RecoveryCodes,
			"updated_at":     model.UpdatedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Two refusals with different remedies: the row is already enabled
		// (disable first), or it is still pending under a NEWER secret (scan
		// the new code). Read once to tell them apart.
		var current TwoFactorModel
		if err := db.Where("user_id = ?", factor.UserID).First(&current).Error; err == nil && current.ConfirmedAt == nil {
			return app_errors.ErrTwoFactorEnrollmentChanged
		}
		return app_errors.ErrTwoFactorAlreadyEnabled
	}
	return nil
}

func (r *TwoFactorRepository) ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, codeHash string) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&TwoFactorModel{}).
		Where("user_id = ? AND ? = ANY(recovery_codes)", userID, codeHash).
		Update("recovery_codes", gorm.Expr("array_remove(recovery_codes, ?)", codeHash))
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// ConsumeTOTPStep records a TOTP time step as spent, refusing it if the stored
// ledger has already reached it. Returns false when the step was replayed.
//
// The shape is copied from ConsumeRecoveryCode above and for the same stated
// reason: single use is decided by the DATABASE, not by a read-then-write from a
// value fetched moments ago, or two concurrent submissions of one code would
// both succeed.
//
// The predicate is strictly-greater (the RFC-recommended form), which closes
// replay in both directions. Known cost, accepted deliberately: with +/-1 skew a
// legitimate later code can occasionally land on an equal-or-earlier step and be
// refused, and the user retries with the next code. The weaker alternative —
// rejecting only an exact repeat — is the fallback if that proves noisy.
func (r *TwoFactorRepository) ConsumeTOTPStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&TwoFactorModel{}).
		Where("user_id = ? AND (last_used_step IS NULL OR last_used_step < ?)", userID, step).
		Update("last_used_step", step)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RecordTOTPStep stores a matched step WITHOUT checking it. Used by enrolment
// confirmation, which validates against the pending secret and cannot itself be
// replayed — but recording closes the enrolment-code-to-Verify hop.
func (r *TwoFactorRepository) RecordTOTPStep(ctx context.Context, userID uuid.UUID, step int64) error {
	return r.db.WithContext(ctx).
		Model(&TwoFactorModel{}).
		Where("user_id = ? AND (last_used_step IS NULL OR last_used_step < ?)", userID, step).
		Update("last_used_step", step).Error
}

func (r *TwoFactorRepository) Delete(ctx context.Context, userID uuid.UUID) error {
	return r.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&TwoFactorModel{}).Error
}

// RemoveEnabledAndAdvanceGeneration: see the port. The row lock comes first
// so the removal queues behind, or ahead of, every other credential
// transition on the identity (refresh rotation, issuance, revocation), and
// the generation moves in the same transaction as the delete: a session
// minted against the old epoch is refused by Issue and Rotate whichever
// side of this transaction it lands on.
func (r *TwoFactorRepository) RemoveEnabledAndAdvanceGeneration(ctx context.Context, userID uuid.UUID) (bool, error) {
	removed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserRow(tx, userID); err != nil {
			return err
		}
		res := tx.Where("user_id = ? AND confirmed_at IS NOT NULL", userID).Delete(&TwoFactorModel{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		upd := tx.Model(&UserModel{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"credential_generation": gorm.Expr("credential_generation + 1"),
			"updated_at":            time.Now(),
		})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			return app_errors.ErrUserNotFound
		}
		removed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}
