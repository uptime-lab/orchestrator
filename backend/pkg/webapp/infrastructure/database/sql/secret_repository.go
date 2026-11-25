package sql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"gorm.io/gorm"
)

var _ repository.SecretRepository = (*SecretRepository)(nil)

// Encryptor is the subset of the crypto encryptor the repository needs.
type Encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// SecretRepository stores secrets, encrypting the value at rest.
type SecretRepository struct {
	db        *gorm.DB
	encryptor Encryptor
}

func NewSecretRepository(db *gorm.DB, encryptor Encryptor) *SecretRepository {
	if encryptor == nil {
		panic("SecretRepository requires an encryptor")
	}
	return &SecretRepository{db: db, encryptor: encryptor}
}

func (r *SecretRepository) Setup() error {
	// Dedupe names before AutoMigrate adds the unique (account_id, name) index:
	// the oldest row keeps its name, younger duplicates get a short id-tail
	// suffix. Idempotent; only relevant for pre-existing data.
	if r.db.Migrator().HasTable(&SecretModel{}) {
		err := r.db.Exec(`UPDATE secrets SET name = name || '-' || left(id::text, 8) WHERE id IN (
			SELECT id FROM (
				SELECT id, row_number() OVER (PARTITION BY account_id, name ORDER BY created_at ASC, id ASC) AS rn
				FROM secrets
			) d WHERE d.rn > 1)`).Error
		if err != nil {
			return err
		}
	}
	if err := r.db.AutoMigrate(&SecretModel{}); err != nil {
		return err
	}
	// GIN index for tag containment filters (tags @> ARRAY[...]).
	return r.db.Exec(`CREATE INDEX IF NOT EXISTS idx_secrets_tags ON secrets USING gin (tags)`).Error
}

func (r *SecretRepository) Create(ctx context.Context, s *secret.Secret) error {
	model := &SecretModel{}
	model.FromDomain(s)
	enc, err := r.encryptor.Encrypt(s.Value)
	if err != nil {
		return err
	}
	model.ValueEncrypted = enc
	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return app_errors.ErrSecretNameTaken
		}
		return err
	}
	return nil
}

// SecretLockClass is the advisory-lock class for per-account secret quota
// mutations ("ORCS"). It shares the lock space with ImageLockClass,
// MemberLockClass and TrackedFileLockClass, so the class word is what keeps them
// from colliding.
const SecretLockClass = 0x4F524353

// CreateUnderCap creates a secret only while the account is below its cap,
// holding a per-account advisory lock across the count and the insert.
//
// The read-then-write it replaces was not merely theoretical here: the workflow
// engine drives server.attribute_upsert against up to
// WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT execution nodes CONCURRENTLY, and node
// attributes are stored as secrets — so a single fan-out had hundreds of
// goroutines all reading the same count before any of them inserted, and the cap
// bounded nothing. The image count cap solved exactly this shape with
// CreateCaptureUnderCap; the pattern was never applied here.
//
// maxPerAccount <= 0 means unlimited and takes the plain path, so an install
// with no cap pays nothing for the lock.
func (r *SecretRepository) CreateUnderCap(ctx context.Context, s *secret.Secret, maxPerAccount int) error {
	if maxPerAccount <= 0 {
		return r.Create(ctx, s)
	}
	model := &SecretModel{}
	model.FromDomain(s)
	enc, err := r.encryptor.Encrypt(s.Value)
	if err != nil {
		return err
	}
	model.ValueEncrypted = enc

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
			SecretLockClass, s.AccountID.String()).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&SecretModel{}).Where("account_id = ?", s.AccountID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxPerAccount) {
			return fmt.Errorf("%w: %d of %d in use. Note that node attributes are stored as secrets and count toward this limit.",
				app_errors.ErrSecretLimitReached, count, maxPerAccount)
		}
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return app_errors.ErrSecretNameTaken
			}
			return err
		}
		return nil
	})
}

// secretUpdateColumns are the columns Update writes. The repository test holds
// every model column to this list or to its named exceptions.
var secretUpdateColumns = []string{"name", "kind", "key", "value_encrypted", "sensitive", "tags", "updated_at"}

func (r *SecretRepository) Update(ctx context.Context, s *secret.Secret) error {
	model := &SecretModel{}
	model.FromDomain(s)
	model.UpdatedAt = time.Now()
	enc, err := r.encryptor.Encrypt(s.Value)
	if err != nil {
		return err
	}
	model.ValueEncrypted = enc

	// Select the mutable columns explicitly so an empty value is written. "kind"
	// MUST be here: secret_upsert and the update command both advertise kind as
	// editable, but it was missing from this whitelist, so an opaque->env change
	// reported success while persisting nothing — and only env-kind secrets reach a
	// device, so the variable silently never arrived. "group_id" was a
	// phantom: SecretModel has no such field (groups were retired for tags and the
	// column is dropped in Setup()), so it selected nothing and only misled.
	result := r.db.WithContext(ctx).
		Model(&SecretModel{}).
		Where("id = ? AND account_id = ?", s.ID, s.AccountID).
		Select(secretUpdateColumns).
		Updates(model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return app_errors.ErrSecretNameTaken
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrSecretNotFound
	}
	return nil
}

func (r *SecretRepository) GetByID(ctx context.Context, accountID, id uuid.UUID) (*secret.Secret, error) {
	var model SecretModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, app_errors.ErrSecretNotFound
		}
		return nil, result.Error
	}
	return r.toDomainDecrypted(&model), nil
}

func (r *SecretRepository) GetByIDs(ctx context.Context, accountID uuid.UUID, ids []uuid.UUID) ([]secret.Secret, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var models []SecretModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND id IN ?", accountID, ids).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	return r.toDomainDecryptedSlice(models), nil
}

func (r *SecretRepository) GetByName(ctx context.Context, accountID uuid.UUID, name string) (*secret.Secret, error) {
	var model SecretModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND name = ?", accountID, name).
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, app_errors.ErrSecretNotFound
		}
		return nil, result.Error
	}
	return r.toDomainDecrypted(&model), nil
}

func (r *SecretRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]secret.Secret, error) {
	var models []SecretModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at ASC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	return r.toDomainDecryptedSlice(models), nil
}

func (r *SecretRepository) List(ctx context.Context, accountID uuid.UUID, filter repository.SecretListFilter) ([]secret.Secret, int64, error) {
	q := r.db.WithContext(ctx).Model(&SecretModel{}).Where("account_id = ?", accountID)
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("name ILIKE ? OR key ILIKE ?", like, like)
	}
	if filter.Kind != "" {
		q = q.Where("kind = ?", filter.Kind)
	}
	if filter.Sensitive != nil {
		q = q.Where("sensitive = ?", *filter.Sensitive)
	}
	if len(filter.Tags) > 0 {
		// Tags are charset-restricted in the domain, safe for an array literal.
		q = q.Where("tags @> ?::text[]", "{"+strings.Join(filter.Tags, ",")+"}")
	}
	// Delivery predicate: an attachment reaches this secret directly or via
	// its group.
	const subjectMatch = "(a.secret_id = secrets.id OR (a.tag <> '' AND secrets.tags @> ARRAY[a.tag]::text[]))"
	if filter.OUID != nil {
		q = q.Where(`EXISTS (
			SELECT 1 FROM secret_attachments a
			WHERE a.account_id = secrets.account_id AND `+subjectMatch+`
			  AND (a.ou_id = ? OR a.node_id IN (SELECT n.id FROM nodes n WHERE n.account_id = secrets.account_id AND n.ou_id = ?))
		)`, *filter.OUID, *filter.OUID)
	}
	if filter.NodeID != nil {
		q = q.Where(`EXISTS (
			SELECT 1 FROM secret_attachments a
			WHERE a.account_id = secrets.account_id AND `+subjectMatch+`
			  AND a.node_id = ?
		)`, *filter.NodeID)
	}
	if filter.Unattached {
		q = q.Where(`NOT EXISTS (
			SELECT 1 FROM secret_attachments a
			WHERE a.account_id = secrets.account_id AND ` + subjectMatch + `
		)`)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Sort/order are validated by the filter type itself (SortColumn /
	// OrderDirection), so every SecretListFilter consumer shares the guard.
	q = q.Order(filter.SortColumn() + " " + filter.OrderDirection())
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit).Offset(filter.Offset)
	}

	var models []SecretModel
	if err := q.Find(&models).Error; err != nil {
		return nil, 0, err
	}
	return r.toDomainDecryptedSlice(models), total, nil
}

func (r *SecretRepository) TagCounts(ctx context.Context, accountID uuid.UUID) (map[string]int, error) {
	var rows []struct {
		Tag   string
		Count int
	}
	err := r.db.WithContext(ctx).
		Raw(`SELECT unnest(tags) AS tag, count(*) AS count FROM secrets WHERE account_id = ? GROUP BY tag`, accountID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Tag] = row.Count
	}
	return counts, nil
}

func (r *SecretRepository) CountUnattached(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM secrets s WHERE s.account_id = ? AND NOT EXISTS (
			SELECT 1 FROM secret_attachments a
			WHERE a.account_id = s.account_id
			  AND (a.secret_id = s.id OR (a.tag <> '' AND s.tags @> ARRAY[a.tag]::text[]))
		)`, accountID).
		Scan(&count).Error
	return count, err
}

func (r *SecretRepository) CountAll(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&SecretModel{}).
		Where("account_id = ?", accountID).
		Count(&count).Error
	return count, err
}

func (r *SecretRepository) GetByTags(ctx context.Context, accountID uuid.UUID, tagList []string) ([]secret.Secret, error) {
	if len(tagList) == 0 {
		return nil, nil
	}
	var models []SecretModel
	// Overlap operator: any secret carrying at least one of the tags. Tags are
	// charset-restricted in the domain, safe for an array literal.
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND tags && ?::text[]", accountID, "{"+strings.Join(tagList, ",")+"}").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	return r.toDomainDecryptedSlice(models), nil
}

func (r *SecretRepository) TouchLastUsed(ctx context.Context, accountID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	// Throttled in SQL: only rows not touched within the last hour are updated.
	return r.db.WithContext(ctx).Exec(
		`UPDATE secrets SET last_used_at = now()
		 WHERE account_id = ? AND id IN ?
		   AND (last_used_at IS NULL OR last_used_at < now() - interval '1 hour')`,
		accountID, ids).Error
}

func (r *SecretRepository) Delete(ctx context.Context, accountID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		Delete(&SecretModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrSecretNotFound
	}
	return nil
}

// DeleteNodeAttributes removes every attribute-backing secret of a node. Zero
// rows is not an error — most nodes have no attributes. The prefix is a uuid
// plus fixed separators, so the LIKE pattern carries no wildcard characters
// besides the trailing %.
func (r *SecretRepository) DeleteNodeAttributes(ctx context.Context, accountID, nodeID uuid.UUID) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND name LIKE ?", accountID, secret.NodeAttributeNamePrefix(nodeID)+"%").
		Delete(&SecretModel{})
	return result.RowsAffected, result.Error
}

// toDomainDecryptedSlice maps rows to domain secrets, decrypting values.
func (r *SecretRepository) toDomainDecryptedSlice(models []SecretModel) []secret.Secret {
	secrets := make([]secret.Secret, len(models))
	for i := range models {
		secrets[i] = *r.toDomainDecrypted(&models[i])
	}
	return secrets
}

// toDomainDecrypted maps a row to a domain secret, decrypting the value.
//
// A value that decrypts with none of the configured keys is NOT an error to the
// caller: it degrades to an empty Value plus ValueUnreadable, the same contract
// decryptProvisioningPassword carries. Aborting instead took the whole read
// down — one bad row after a rotation that dropped SECRETS_ENCRYPTION_KEY_OLD
// too early 500'd every page of the Secrets list containing it AND the GetByID
// behind the edit form, so the only way to find or repair the row was psql. The
// flag travels with the secret and every delivery path refuses on it, so this
// widens what can be READ, not what can be sent to a device.
func (r *SecretRepository) toDomainDecrypted(model *SecretModel) *secret.Secret {
	d := model.ToDomain()
	if model.ValueEncrypted == "" {
		return d
	}
	plaintext, err := r.encryptor.Decrypt(model.ValueEncrypted)
	if err != nil {
		// looks_like_envelope separates the two causes an operator acts on
		// differently: true means a key problem, false means the column holds
		// something that was never this format.
		slog.Error("stored secret value could not be decrypted with any configured key; reporting it as unreadable",
			"subject", "secrets."+model.ID.String(),
			"account_id", model.AccountID,
			"looks_like_envelope", node_file.LooksLikeStoredCiphertext(model.ValueEncrypted),
			"error", err)
		d.ValueUnreadable = true
		return d
	}
	d.Value = plaintext
	return d
}
