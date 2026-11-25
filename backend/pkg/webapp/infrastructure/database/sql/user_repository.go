package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.UserRepository = (*UserRepository)(nil)

// UserRepository is the implementation of the user repository using SQL
type UserRepository struct {
	db *gorm.DB
}

// autoMigrate creates the necessary database tables
func (r *UserRepository) autoMigrate() error {
	return r.db.AutoMigrate(&UserModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *UserRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	// Generations start at 1 (see user.NewUser): the rows created before the
	// column existed, or by the first deploy that carried it with default 0,
	// are lifted once so that a 0 on a token means only "minted before the
	// claim". Idempotent; a row at 1 or above is never touched.
	if err := r.db.Exec(`UPDATE users SET credential_generation = 1 WHERE credential_generation = 0`).Error; err != nil {
		return fmt.Errorf("backfilling credential_generation: %w", err)
	}
	// The legacy account_id column (absent from UserModel, so AutoMigrate
	// never touches it) was created NOT NULL; users created through invites
	// have no legacy account, so the constraint must go. Fresh installs never
	// get the column at all.
	if r.db.Migrator().HasColumn(&UserModel{}, "account_id") {
		if err := r.db.Exec(`ALTER TABLE users ALTER COLUMN account_id DROP NOT NULL`).Error; err != nil {
			return err
		}
	}
	// Clerk was retired in favor of external_identities; drop the now-dead
	// clerk_id column (and its unique index) if it still exists.
	if r.db.Migrator().HasColumn(&UserModel{}, "clerk_id") {
		if err := r.db.Exec(`ALTER TABLE users DROP COLUMN IF EXISTS clerk_id`).Error; err != nil {
			return err
		}
	}
	return r.normalizeEmails()
}

// normalizeEmails lowercases any mixed-case email rows once, so the byte-wise
// unique index behaves like a lower(email) index and OIDC/invite link-by-email
// cannot miss an existing identity. It fails loudly if two rows would collapse
// to the same address (a pre-existing duplicate that must be resolved by hand).
func (r *UserRepository) normalizeEmails() error {
	var collisions []string
	err := r.db.Raw(`SELECT lower(email) FROM users GROUP BY lower(email) HAVING count(*) > 1`).Scan(&collisions).Error
	if err != nil {
		return err
	}
	if len(collisions) > 0 {
		return fmt.Errorf("cannot normalize emails: case-insensitive duplicates exist, resolve manually: %v", collisions)
	}
	return r.db.Exec(`UPDATE users SET email = lower(email) WHERE email <> lower(email)`).Error
}

// NewUserRepository creates a new user repository
func NewUserRepository(db *gorm.DB) *UserRepository {
	repo := &UserRepository{
		db: db,
	}
	return repo
}

// Create creates a new user
func (r *UserRepository) Create(ctx context.Context, user *user.User) error {
	model := &UserModel{}
	model.FromDomain(user)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// GetByID retrieves a user by ID
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	var model UserModel
	result := r.db.WithContext(ctx).Where("id = ?", id).First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrUserNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// GetByEmail retrieves a user by email address (case-insensitive: the query
// email is normalized to match the canonical stored form).
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	var model UserModel
	result := r.db.WithContext(ctx).Where("email = ?", user.NormalizeEmail(email)).First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrUserNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// ListByEmailIgnoringCase: see the port. lower(email) rather than the
// stored-form equality GetByEmail uses, so a row that boot-time
// normalization never reached is still counted.
func (r *UserRepository) ListByEmailIgnoringCase(ctx context.Context, email string) ([]*user.User, error) {
	var models []UserModel
	if err := r.db.WithContext(ctx).Where("lower(email) = ?", user.NormalizeEmail(email)).Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	users := make([]*user.User, 0, len(models))
	for i := range models {
		users = append(users, models[i].ToDomain())
	}
	return users, nil
}

// Update updates an existing user
func (r *UserRepository) Update(ctx context.Context, user *user.User) error {
	model := &UserModel{}
	model.FromDomain(user)

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).Where("id = ?", user.ID), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrUserNotFound
	}

	return nil
}

// Delete deletes a user by UUID
func (r *UserRepository) Delete(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&UserModel{})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrUserNotFound
	}

	return nil
}

// ListByAccountID retrieves the account's members (via account_members) with
// pagination.
func (r *UserRepository) ListByAccountID(ctx context.Context, accountID uuid.UUID, offset, limit int) ([]*user.User, int, error) {
	var models []UserModel
	var count int64

	membership := r.db.WithContext(ctx).Model(&UserModel{}).
		Joins("JOIN account_members am ON am.user_id = users.id").
		Where("am.account_id = ?", accountID)

	if err := membership.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	result := membership.
		Order("users.created_at ASC").
		Offset(offset).
		Limit(limit).
		Find(&models)

	if result.Error != nil {
		return nil, 0, result.Error
	}

	// Convert to domain models
	users := make([]*user.User, len(models))
	for i, model := range models {
		users[i] = model.ToDomain()
	}

	return users, int(count), nil
}

// The column-specific transitions. Each is ONE statement
// against named columns: nothing here can carry a stale copy of another
// column, and the credential generation advances inside the same statement
// as the credential it describes, so a predicate elsewhere sees the two move
// together or not at all.

func (r *UserRepository) SetPassword(ctx context.Context, id uuid.UUID, hashedPassword string, changedAt time.Time) error {
	res := r.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"hashed_password":       hashedPassword,
		"password_changed_at":   changedAt,
		"credential_generation": gorm.Expr("credential_generation + 1"),
		"updated_at":            time.Now(),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrUserNotFound
	}
	return nil
}

// SetPasswordIfEmail: see the port. The address is in the WHERE, so the
// statement itself refuses once the row has moved to another mailbox; a
// service-side comparison a moment earlier is advice.
func (r *UserRepository) SetPasswordIfEmail(ctx context.Context, id uuid.UUID, hashedPassword, expectedEmail string, changedAt time.Time) error {
	res := r.db.WithContext(ctx).Model(&UserModel{}).
		Where("id = ? AND email = ?", id, user.NormalizeEmail(expectedEmail)).
		Updates(map[string]interface{}{
			"hashed_password":       hashedPassword,
			"password_changed_at":   changedAt,
			"credential_generation": gorm.Expr("credential_generation + 1"),
			"updated_at":            time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Two refusals, two remedies: the identity is gone, or it is now
		// reached through another address and this grant proves nothing.
		var n int64
		if err := r.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return app_errors.ErrUserNotFound
		}
		return app_errors.ErrPasswordResetStale
	}
	return nil
}

// UpdateProfile: see the port. One UPDATE under the user's row lock. An
// omitted field is COALESCEd to the stored column, and "did the email
// change" is `IS DISTINCT FROM` against the stored column INSIDE the
// statement — the SET expressions read the row as it is at write time, not
// as the caller saw it. The row lock is taken first because an email change
// is a credential transition (it advances the generation), so it queues
// behind a rotation or a revocation exactly as a password write does; the
// locked read also supplies the previous address the service notifies.
func (r *UserRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name, email *string) (repository.ProfileChange, error) {
	var change repository.ProfileChange
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockUserRow(tx, id)
		if err != nil {
			return err
		}
		change, err = updateProfileLocked(tx, locked, name, email)
		return err
	})
	return change, err
}

// UpdateProfileWhileSessionLive: see the port. The same statement, behind
// lockLiveSession instead of the bare row lock:
// the address is the sign-in identifier, and a request whose session died
// between the step-up guard and this write must not repoint it.
func (r *UserRepository) UpdateProfileWhileSessionLive(ctx context.Context, id uuid.UUID, name, email *string, familyID uuid.UUID, issuedAt time.Time, generation int64) (repository.ProfileChange, error) {
	var change repository.ProfileChange
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockLiveSession(tx, id, familyID, issuedAt, generation)
		if err != nil {
			return err
		}
		change, err = updateProfileLocked(tx, locked, name, email)
		return err
	})
	return change, err
}

// SetPasswordWhileSessionLive: see the port. SetPassword's statement behind
// lockLiveSession: a password change from a
// session that was signed out, or from a token minted before a recovery,
// must not land after the guard let it through.
func (r *UserRepository) SetPasswordWhileSessionLive(ctx context.Context, id uuid.UUID, hashedPassword string, changedAt time.Time, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockLiveSession(tx, id, familyID, issuedAt, generation); err != nil {
			return err
		}
		res := tx.Model(&UserModel{}).Where("id = ?", id).Updates(map[string]interface{}{
			"hashed_password":       hashedPassword,
			"password_changed_at":   changedAt,
			"credential_generation": gorm.Expr("credential_generation + 1"),
			"updated_at":            time.Now(),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return app_errors.ErrUserNotFound
		}
		return nil
	})
}

// updateProfileLocked is the ONE profile statement, run by both variants
// above against a row the caller already holds the lock on.
func updateProfileLocked(tx *gorm.DB, locked *lockedUser, name, email *string) (repository.ProfileChange, error) {
	var normalized *string
	if email != nil {
		n := user.NormalizeEmail(*email)
		normalized = &n
	}
	var change repository.ProfileChange
	err := func() error {
		id := locked.ID
		var after struct {
			Email                string
			CredentialGeneration int64
		}
		res := tx.Raw(`
			UPDATE users SET
				name = COALESCE(?::text, name),
				email = COALESCE(?::text, email),
				email_verified_at = CASE
					WHEN ?::text IS NOT NULL AND ?::text IS DISTINCT FROM email THEN NULL
					ELSE email_verified_at END,
				credential_generation = CASE
					WHEN ?::text IS NOT NULL AND ?::text IS DISTINCT FROM email THEN credential_generation + 1
					ELSE credential_generation END,
				updated_at = ?
			WHERE id = ?
			RETURNING email, credential_generation`,
			name, normalized, normalized, normalized, normalized, normalized, time.Now(), id).Scan(&after)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return app_errors.ErrUserNotFound
		}
		change = repository.ProfileChange{
			// The statement's own verdict: the generation moved exactly when
			// it decided the address changed.
			EmailChanged:  after.CredentialGeneration != locked.CredentialGeneration,
			PreviousEmail: locked.Email,
		}
		return nil
	}()
	return change, err
}

func (r *UserRepository) MarkEmailVerified(ctx context.Context, id uuid.UUID, email string, at time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Model(&UserModel{}).
		Where("id = ? AND lower(email) = lower(?) AND email_verified_at IS NULL", id, email).
		Updates(map[string]interface{}{"email_verified_at": at, "updated_at": time.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (r *UserRepository) AcknowledgeBetaNotice(ctx context.Context, id uuid.UUID, version int, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"beta_notice_ack_at":      at,
		"beta_notice_ack_version": version,
		"updated_at":              time.Now(),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrUserNotFound
	}
	return nil
}

// lockUserRow serialises credential transitions on one identity: it takes the
// user's row lock inside tx and hands back the columns a transition predicate
// needs. Refresh rotation, session revocation and identity attachment all
// take it, so "the session was revoked" and "a successor was minted" can
// never both be true of the same instant. A
// password write is a single UPDATE on the same row and therefore queues
// behind whoever holds it.
type lockedUser struct {
	ID                   uuid.UUID
	Email                string
	PasswordChangedAt    *time.Time
	CredentialGeneration int64
}

func lockUserRow(tx *gorm.DB, userID uuid.UUID) (*lockedUser, error) {
	var row lockedUser
	res := tx.Raw(`SELECT id, email, password_changed_at, credential_generation FROM users WHERE id = ? FOR UPDATE`, userID).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 || row.ID == uuid.Nil {
		return nil, app_errors.ErrUserNotFound
	}
	return &row, nil
}

// credentialEpochMoved is the row-lock half of the policy the auth service
// applies to a token: it predates a password change, or (for a token that
// carries the claim — 0 is one minted before it existed) its generation is
// not the row's.
func (u *lockedUser) credentialEpochMoved(issuedAt time.Time, generation int64) bool {
	if u.PasswordChangedAt != nil && issuedAt.Before(*u.PasswordChangedAt) {
		return true
	}
	return generation > 0 && u.CredentialGeneration != generation
}

// lockLiveSession is the ONE session-bound persistence boundary:
// it takes the user's row lock,
// refuses a token from a dead credential epoch and refuses a family that is
// no longer live, and hands the locked row back so the caller can write
// inside the same transaction. Every write that makes a durable way to sign
// in — an OIDC identity, a passkey, an enabled second factor — goes through
// it, so a revocation (which takes the same lock) has either already ended
// the family, refusing the write, or runs after the credential exists and
// is at least visible to whoever asks next. A nil family or a zero issue
// time is refused outright: an absent proof is not a live session.
func lockLiveSession(tx *gorm.DB, userID, familyID uuid.UUID, issuedAt time.Time, generation int64) (*lockedUser, error) {
	if familyID == uuid.Nil || issuedAt.IsZero() {
		return nil, app_errors.ErrTokenInvalid
	}
	locked, err := lockUserRow(tx, userID)
	if err != nil {
		return nil, err
	}
	if locked.credentialEpochMoved(issuedAt, generation) {
		return nil, app_errors.ErrTokenInvalid
	}
	fam, err := readFamily(tx, userID, familyID)
	if err != nil {
		return nil, err
	}
	if !fam.live() {
		return nil, app_errors.ErrTokenInvalid
	}
	return locked, nil
}

// familyState is what the two family predicates — lockLiveSession's and
// Issue's — read, so they cannot drift apart ("no revoked row" and "a live
// row exists" are equal only while access tokens outlive no refresh token).
type familyState struct {
	Total   int64
	Revoked int64
	Live    int64 // unrevoked AND unexpired
}

// live is the ONE definition of a live session family: it has rows, none of
// them revoked, and at least one still redeemable. A revoked row anywhere
// means a logout, a "sign out everywhere", a replay or a cap eviction
// settled it; no redeemable row means it expired.
func (f familyState) live() bool { return f.Total > 0 && f.Revoked == 0 && f.Live > 0 }

func readFamily(tx *gorm.DB, userID, familyID uuid.UUID) (familyState, error) {
	var fam familyState
	err := tx.Raw(`
		SELECT count(*) AS total,
		       count(*) FILTER (WHERE revoked_at IS NOT NULL) AS revoked,
		       count(*) FILTER (WHERE revoked_at IS NULL AND expires_at > ?) AS live
		FROM refresh_tokens WHERE user_id = ? AND family_id = ?`, time.Now(), userID, familyID).Scan(&fam).Error
	return fam, err
}
