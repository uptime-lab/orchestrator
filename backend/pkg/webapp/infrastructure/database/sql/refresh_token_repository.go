package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.RefreshTokenRepository = (*RefreshTokenRepository)(nil)

// RefreshTokenModel is the GORM model for issued refresh credentials.
type RefreshTokenModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	AccountID uuid.UUID  `gorm:"column:account_id;type:uuid;not null;index"`
	FamilyID  uuid.UUID  `gorm:"column:family_id;type:uuid;not null;index"`
	TokenHash string     `gorm:"column:token_hash;size:64;not null;uniqueIndex"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	// Session identification for the user's own "where am I signed in" list.
	// Self-reported by the client; never read back as an authorization input.
	UserAgent string `gorm:"column:user_agent;size:255"`
	IP        string `gorm:"column:ip;size:64"`
}

func (RefreshTokenModel) TableName() string {
	return "refresh_tokens"
}

func (m *RefreshTokenModel) ToDomain() *refresh_token.RefreshToken {
	return &refresh_token.RefreshToken{
		ID:        m.ID,
		UserID:    m.UserID,
		AccountID: m.AccountID,
		FamilyID:  m.FamilyID,
		TokenHash: m.TokenHash,
		ExpiresAt: m.ExpiresAt,
		UsedAt:    m.UsedAt,
		RevokedAt: m.RevokedAt,
		CreatedAt: m.CreatedAt,
		UserAgent: m.UserAgent,
		IP:        m.IP,
	}
}

func refreshTokenFromDomain(t *refresh_token.RefreshToken) *RefreshTokenModel {
	return &RefreshTokenModel{
		ID:        t.ID,
		UserID:    t.UserID,
		AccountID: t.AccountID,
		FamilyID:  t.FamilyID,
		TokenHash: t.TokenHash,
		ExpiresAt: t.ExpiresAt,
		UsedAt:    t.UsedAt,
		RevokedAt: t.RevokedAt,
		CreatedAt: t.CreatedAt,
		UserAgent: t.UserAgent,
		IP:        t.IP,
	}
}

// RefreshTokenRepository is the GORM implementation.
type RefreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) *RefreshTokenRepository {
	if db == nil {
		panic("RefreshTokenRepository requires a database handle")
	}
	return &RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) Setup() error {
	return r.db.AutoMigrate(&RefreshTokenModel{})
}

func (r *RefreshTokenRepository) Create(ctx context.Context, token *refresh_token.RefreshToken) error {
	return r.db.WithContext(ctx).Create(refreshTokenFromDomain(token)).Error
}

// Issue: see the port. The insert that starts a session used to be a bare
// Create: a password verified before an administrative reset could then
// store a session after it, and an account switch that had
// just proven its family live could store a sibling after a revocation
// ended that family. Both checks run under the
// user's row lock, which every revocation and every credential write take.
func (r *RefreshTokenRepository) Issue(ctx context.Context, token *refresh_token.RefreshToken, generation int64, passwordChangedAt *time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockUserRow(tx, token.UserID)
		if err != nil {
			return err
		}
		// The epoch the pair was minted from is the one that authenticated;
		// a row that has moved on means the credential this login proved has
		// been replaced meanwhile. BOTH columns are compared: the generation
		// covers every transition the backend writes, but a reset written by
		// an operator tool that predates the generation advance moves only
		// password_changed_at — and the pair's own mint
		// time is useless here, since it is minted AFTER such a reset and
		// would pass a "newer than the change" test.
		if generation > 0 && locked.CredentialGeneration != generation {
			return app_errors.ErrTokenInvalid
		}
		if !sameInstant(locked.PasswordChangedAt, passwordChangedAt) {
			return app_errors.ErrTokenInvalid
		}
		// A fresh login has no rows and passes; a sibling for an existing
		// family requires that family to be live in exactly the sense
		// lockLiveSession uses (no revoked row, one still redeemable), because
		// a revocation that settled it is final.
		fam, err := readFamily(tx, token.UserID, token.FamilyID)
		if err != nil {
			return err
		}
		if fam.Total > 0 && !fam.live() {
			return app_errors.ErrTokenInvalid
		}
		return tx.Create(refreshTokenFromDomain(token)).Error
	})
}

// sameInstant compares two nullable timestamps as the column stores them:
// timestamptz keeps microseconds, a Go value may carry nanoseconds and a
// monotonic reading, so both are rounded to the column's precision and
// compared with Equal. nil equals only nil.
func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Round(time.Microsecond).Equal(b.Round(time.Microsecond))
}

// Claim redeems a token in a single conditional UPDATE.
//
// The distinction between "already used" and "does not exist" matters and is
// deliberately preserved here, unlike the public one-time-token endpoints where
// a uniform answer avoids an oracle. This caller is already authenticated by
// possession of a signed JWT, and the difference is what turns a replay into a
// detected theft rather than a plain rejection.
func (r *RefreshTokenRepository) Claim(ctx context.Context, tokenHash string) (*refresh_token.RefreshToken, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&RefreshTokenModel{}).
		Where("token_hash = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", tokenHash, now).
		Update("used_at", now)
	if result.Error != nil {
		return nil, result.Error
	}

	var model RefreshTokenModel
	if err := r.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrTokenInvalid
		}
		return nil, err
	}

	if result.RowsAffected == 0 {
		// The row exists but did not qualify. A spent-but-unrevoked token is a
		// REPLAY: a legitimate client never presents one twice, so two parties
		// hold it. Everything else (revoked, expired) is an ordinary rejection.
		if model.UsedAt != nil && model.RevokedAt == nil && model.ExpiresAt.After(now) {
			return model.ToDomain(), app_errors.ErrRefreshTokenReused
		}
		return nil, app_errors.ErrTokenInvalid
	}
	return model.ToDomain(), nil
}

// errRefreshReplay carries a replayed row out of the rotation transaction so
// the claim's write is rolled back and the caller still learns whose family
// to revoke.
type errRefreshReplay struct{ row *refresh_token.RefreshToken }

func (errRefreshReplay) Error() string { return "refresh token replayed" }

// Rotate: see the port. Every step runs inside one transaction that holds
// the user's row lock, which every revocation below takes as well.
func (r *RefreshTokenRepository) Rotate(ctx context.Context, tokenHash string, issuedAt time.Time, generation int64, successor *refresh_token.RefreshToken) (*refresh_token.RefreshToken, error) {
	var claimed *refresh_token.RefreshToken
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A first read only to learn WHOSE lock to take; the row is read
		// again under the lock below, because a concurrent presentation of
		// the same token that won the lock has claimed it meanwhile, and a
		// replay must be classified from THAT state, not from this snapshot
		// (a stale snapshot would turn a thief racing the legitimate client
		// into a plain "invalid token" that revokes nothing).
		var owner RefreshTokenModel
		if err := tx.Where("token_hash = ?", tokenHash).First(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return app_errors.ErrTokenInvalid
			}
			return err
		}
		locked, err := lockUserRow(tx, owner.UserID)
		if err != nil {
			return err
		}
		var model RefreshTokenModel
		if err := tx.Where("token_hash = ?", tokenHash).First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return app_errors.ErrTokenInvalid
			}
			return err
		}
		// The credential epoch, re-read UNDER the lock: a password change
		// or an email change that committed after the service's own check
		// is visible here.
		if locked.PasswordChangedAt != nil && issuedAt.Before(*locked.PasswordChangedAt) {
			return app_errors.ErrTokenInvalid
		}
		if generation > 0 && locked.CredentialGeneration != generation {
			return app_errors.ErrTokenInvalid
		}
		now := time.Now()
		res := tx.Model(&RefreshTokenModel{}).
			Where("token_hash = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", tokenHash, now).
			Update("used_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if model.UsedAt != nil && model.RevokedAt == nil && model.ExpiresAt.After(now) {
				return errRefreshReplay{row: model.ToDomain()}
			}
			return app_errors.ErrTokenInvalid
		}
		// The family must be whole: one revoked row in it means a logout, a
		// "sign out everywhere", a replay or a cap eviction settled it, and a
		// successor would resurrect exactly what that settled.
		var revoked int64
		if err := tx.Model(&RefreshTokenModel{}).
			Where("family_id = ? AND revoked_at IS NOT NULL", model.FamilyID).
			Count(&revoked).Error; err != nil {
			return err
		}
		if revoked > 0 {
			return app_errors.ErrTokenInvalid
		}
		if err := tx.Create(refreshTokenFromDomain(successor)).Error; err != nil {
			return err
		}
		claimed = model.ToDomain()
		return nil
	})
	var replay errRefreshReplay
	if errors.As(err, &replay) {
		return replay.row, app_errors.ErrRefreshTokenReused
	}
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// The revocations below each take the user's row lock first, so they
// serialise against Rotate: a revocation either runs before
// the rotation, which then finds the family revoked and refuses, or after it,
// and then sees the successor and revokes it too. RevokeFamily learns the
// user from the family itself.

func (r *RefreshTokenRepository) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ UserID uuid.UUID }
		res := tx.Raw(`SELECT user_id FROM refresh_tokens WHERE family_id = ? LIMIT 1`, familyID).Scan(&owner)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // nothing to revoke
		}
		if _, err := lockUserRow(tx, owner.UserID); err != nil && !errors.Is(err, app_errors.ErrUserNotFound) {
			return err
		}
		return tx.Model(&RefreshTokenModel{}).
			Where("family_id = ? AND revoked_at IS NULL", familyID).
			Update("revoked_at", time.Now()).Error
	})
}

func (r *RefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserRow(tx, userID); err != nil && !errors.Is(err, app_errors.ErrUserNotFound) {
			return err
		}
		return tx.Model(&RefreshTokenModel{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", time.Now()).Error
	})
}

// DeleteExpired prunes rows past their TTL. Spent and revoked rows are kept
// until expiry so a replay is still detectable for the token's whole lifetime —
// deleting them early would turn a detectable theft back into a silent one.
func (r *RefreshTokenRepository) DeleteExpired(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&RefreshTokenModel{})
	return result.RowsAffected, result.Error
}

// sessionRow is the per-family projection ListSessions scans into.
type sessionRow struct {
	FamilyID   uuid.UUID
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// activeFamilySQL projects the live token families of one user, most recently
// active first. A family is live while any of its tokens is unrevoked and
// unexpired; "last used" falls back to creation, because a session that has
// never rotated was still used — to log in.
//
// The identifying columns come from the family's FIRST token: that is the login
// itself. Reading them off the newest token instead would show the device of
// the most recent rotation, which is the same client but a later, less
// meaningful moment.
const activeFamilySQL = `
	SELECT f.family_id,
	       f.created_at,
	       f.last_used_at,
	       f.expires_at,
	       first.user_agent,
	       first.ip
	FROM (
		SELECT family_id,
		       MIN(created_at) AS created_at,
		       MAX(COALESCE(used_at, created_at)) AS last_used_at,
		       MAX(expires_at) AS expires_at
		FROM refresh_tokens
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > now()
		GROUP BY family_id
	) f
	JOIN LATERAL (
		SELECT user_agent, ip
		FROM refresh_tokens
		WHERE family_id = f.family_id
		ORDER BY created_at ASC
		LIMIT 1
	) first ON true
	ORDER BY f.last_used_at DESC`

func (r *RefreshTokenRepository) ListSessions(ctx context.Context, userID uuid.UUID) ([]refresh_token.Session, error) {
	var rows []sessionRow
	if err := r.db.WithContext(ctx).Raw(activeFamilySQL, userID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sessions := make([]refresh_token.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, refresh_token.Session{
			FamilyID:   row.FamilyID,
			UserAgent:  row.UserAgent,
			IP:         row.IP,
			CreatedAt:  row.CreatedAt,
			LastUsedAt: row.LastUsedAt,
			ExpiresAt:  row.ExpiresAt,
		})
	}
	return sessions, nil
}

// RevokeFamilyForUser scopes the revocation to the owner. The family id is
// handed to the client in the session list, so without the user_id predicate
// anyone could revoke anyone's session by id.
func (r *RefreshTokenRepository) RevokeFamilyForUser(ctx context.Context, userID, familyID uuid.UUID) error {
	// Under the user's row lock like its siblings: this is the per-session
	// revoke a user reaches for against a stolen session, and as a bare
	// UPDATE a rotation of that very session could still insert an unrevoked
	// successor beside it.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserRow(tx, userID); err != nil && !errors.Is(err, app_errors.ErrUserNotFound) {
			return err
		}
		res := tx.Model(&RefreshTokenModel{}).
			Where("user_id = ? AND family_id = ? AND revoked_at IS NULL", userID, familyID).
			Update("revoked_at", time.Now())
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Already revoked, expired, or not theirs — all indistinguishable to
			// the caller on purpose, and all mean the same thing: it is not
			// live now.
			return app_errors.ErrTokenInvalid
		}
		return nil
	})
}

func (r *RefreshTokenRepository) RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserRow(tx, userID); err != nil && !errors.Is(err, app_errors.ErrUserNotFound) {
			return err
		}
		return tx.Model(&RefreshTokenModel{}).
			Where("user_id = ? AND family_id <> ? AND revoked_at IS NULL", userID, keepFamilyID).
			Update("revoked_at", time.Now()).Error
	})
}

// EnforceSessionCap revokes the least recently active families past the cap.
//
// Evicting the OLDEST-ACTIVE rather than refusing the new login is deliberate:
// a cap that blocks sign-in turns a forgotten session into a lockout, and the
// person logging in right now is the one present at the keyboard.
func (r *RefreshTokenRepository) EnforceSessionCap(ctx context.Context, userID uuid.UUID, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil // unlimited
	}
	var evicted int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserRow(tx, userID); err != nil && !errors.Is(err, app_errors.ErrUserNotFound) {
			return err
		}
		var families []uuid.UUID
		if err := tx.Raw(`
		SELECT family_id FROM (
			SELECT family_id, MAX(COALESCE(used_at, created_at)) AS last_used_at
			FROM refresh_tokens
			WHERE user_id = ? AND revoked_at IS NULL AND expires_at > now()
			GROUP BY family_id
			ORDER BY last_used_at DESC
			OFFSET ?
		) stale`, userID, keep).Scan(&families).Error; err != nil {
			return err
		}
		if len(families) == 0 {
			return nil
		}
		if err := tx.Model(&RefreshTokenModel{}).
			Where("user_id = ? AND family_id IN ? AND revoked_at IS NULL", userID, families).
			Update("revoked_at", time.Now()).Error; err != nil {
			return err
		}
		evicted = len(families)
		return nil
	})
	return evicted, err
}

func (r *RefreshTokenRepository) AnnotateDevice(ctx context.Context, tokenHash string, device refresh_token.Device) error {
	if device.UserAgent == "" && device.IP == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Model(&RefreshTokenModel{}).
		Where("token_hash = ?", tokenHash).
		Updates(map[string]interface{}{"user_agent": device.UserAgent, "ip": device.IP}).Error
}
