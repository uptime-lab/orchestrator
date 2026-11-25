package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.AccessEntryRepository = (*AccessEntryRepository)(nil)

// AccessEntryRepository persists access entries and their key references.
type AccessEntryRepository struct {
	db        *gorm.DB
	encryptor Encryptor
}

func NewAccessEntryRepository(db *gorm.DB, encryptor Encryptor) *AccessEntryRepository {
	if encryptor == nil {
		panic("AccessEntryRepository requires an encryptor")
	}
	return &AccessEntryRepository{db: db, encryptor: encryptor}
}

func (r *AccessEntryRepository) Setup() error {
	if err := r.db.AutoMigrate(&AccessEntryModel{}, &AccessEntryKeyModel{}, &AccessEntryOUModel{}); err != nil {
		return err
	}
	for _, stmt := range []string{
		// One-time backfill: move the legacy single ou_id column into the
		// access_entry_ous join table (an entry can now live in several units).
		// Idempotent via the composite PK; the column itself is left in place
		// but never read again.
		`INSERT INTO access_entry_ous (access_entry_id, ou_id, account_id)
		   SELECT id, ou_id, account_id FROM access_entries WHERE ou_id IS NOT NULL
		   ON CONFLICT DO NOTHING`,
		// The old per-OU / account-level unique indexes keyed on the ou_id
		// column; with the join table they would misfire (every entry has
		// ou_id NULL now). OU-level username uniqueness is enforced by the
		// service (overlap check across OU sets); node and fleet-wide levels
		// keep DB-level indexes.
		`DROP INDEX IF EXISTS uq_access_entries_user_ou`,
		`DROP INDEX IF EXISTS uq_access_entries_user_account`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_access_entries_user_node ON access_entries (account_id, node_id, username) WHERE type = 'user' AND node_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_access_entries_user_fleet ON access_entries (account_id, username) WHERE type = 'user' AND account_wide`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// Create inserts the entry (encrypting its password) and its initial key refs
// in one transaction.
func (r *AccessEntryRepository) Create(ctx context.Context, e *access_entry.AccessEntry) error {
	model := &AccessEntryModel{}
	model.fromDomain(e)
	if err := r.setEncryptedPassword(model, e); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(model).Error; err != nil {
			// The node/fleet-scope username uniqueness is a DB partial index;
			// translate its violation to a clean domain error (409) instead of
			// surfacing a raw 500. (OU-scope uniqueness is a service-level check.)
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return app_errors.ErrAccessEntryUsernameTaken
			}
			return err
		}
		if err := r.insertOUs(tx, e.AccountID, e.ID, e.OUIDs); err != nil {
			return err
		}
		return r.insertKeys(tx, e.AccountID, e.ID, e.KeyIDs)
	})
}

// accessEntryUpdateColumns are the columns Update writes. The repository test
// holds every model column to this list or to its named exceptions.
var accessEntryUpdateColumns = []string{
	"username", "sudo", "password_encrypted", "adopt_unmanaged", "principals",
	"node_id", "account_wide", "updated_at",
}

// Update rewrites the mutable columns explicitly so a cleared password becomes
// NULL. Key references are managed separately.
func (r *AccessEntryRepository) Update(ctx context.Context, e *access_entry.AccessEntry) error {
	model := &AccessEntryModel{}
	model.fromDomain(e)
	if err := r.setEncryptedPassword(model, e); err != nil {
		return err
	}
	model.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.
			Model(&AccessEntryModel{}).
			Where("id = ? AND account_id = ?", e.ID, e.AccountID).
			// Scope columns (node_id/account_wide) are written too so a re-scope
			// persists; when the caller did not change scope they carry the loaded
			// values, so this is a no-op for a plain field edit.
			Select(accessEntryUpdateColumns).
			Updates(model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrAccessEntryNotFound
		}
		// Replace the OU set wholesale: the domain entity carries the full
		// desired set, so delete-and-reinsert keeps the join table in sync for
		// both plain edits (same set) and re-scopes.
		if err := tx.Where("access_entry_id = ? AND account_id = ?", e.ID, e.AccountID).
			Delete(&AccessEntryOUModel{}).Error; err != nil {
			return err
		}
		return r.insertOUs(tx, e.AccountID, e.ID, e.OUIDs)
	})
}

func (r *AccessEntryRepository) GetByID(ctx context.Context, accountID, id uuid.UUID) (*access_entry.AccessEntry, error) {
	var model AccessEntryModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccessEntryNotFound
		}
		return nil, result.Error
	}
	entry := model.toDomain()
	if err := r.decryptPassword(&model, entry); err != nil {
		return nil, err
	}
	if err := r.hydrateKeys(ctx, accountID, []*access_entry.AccessEntry{entry}); err != nil {
		return nil, err
	}
	if err := r.hydrateOUs(ctx, accountID, []*access_entry.AccessEntry{entry}); err != nil {
		return nil, err
	}
	return entry, nil
}

func (r *AccessEntryRepository) Delete(ctx context.Context, accountID, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("access_entry_id = ? AND account_id = ?", id, accountID).
			Delete(&AccessEntryKeyModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("access_entry_id = ? AND account_id = ?", id, accountID).
			Delete(&AccessEntryOUModel{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ? AND account_id = ?", id, accountID).Delete(&AccessEntryModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrAccessEntryNotFound
		}
		return nil
	})
}

// ouMembership is the EXISTS predicate tying an entry to a unit via the
// access_entry_ous join table.
const ouMembership = `EXISTS (SELECT 1 FROM access_entry_ous aeo WHERE aeo.access_entry_id = access_entries.id AND aeo.ou_id`

func (r *AccessEntryRepository) ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.list(ctx, accountID, "account_id = ? AND "+ouMembership+" = ?)", accountID, ouID)
}

func (r *AccessEntryRepository) ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.list(ctx, accountID, "account_id = ? AND node_id = ?", accountID, nodeID)
}

// ListAccountLevel returns only the account-DEFAULT entries: no-target rows
// that were explicitly marked account-wide. An unassigned entry
// (account_wide = false) applies to no node and is deliberately left out of
// resolution.
func (r *AccessEntryRepository) ListAccountLevel(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.list(ctx, accountID, "account_id = ? AND node_id IS NULL AND account_wide = ?", accountID, true)
}

// ListUnassigned returns no-target entries that are not account-wide — they
// apply to no node and are the default landing bucket for new entries.
func (r *AccessEntryRepository) ListUnassigned(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.list(ctx, accountID,
		"account_id = ? AND node_id IS NULL AND account_wide = ? AND NOT "+ouMembership+" IS NOT NULL)",
		accountID, false)
}

// ListAll returns every entry of the account regardless of scope — the CA trust
// overview needs the full picture (which units trust which CA, with which
// principals) in one call instead of fanning out per scope.
func (r *AccessEntryRepository) ListAll(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.list(ctx, accountID, "account_id = ?", accountID)
}

func (r *AccessEntryRepository) ListByOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) ([]access_entry.AccessEntry, error) {
	if len(ouIDs) == 0 {
		return nil, nil
	}
	return r.list(ctx, accountID, "account_id = ? AND "+ouMembership+" IN ?)", accountID, ouIDs)
}

// list runs a scoped SELECT, decrypts passwords and hydrates key references.
func (r *AccessEntryRepository) list(ctx context.Context, accountID uuid.UUID, where string, args ...interface{}) ([]access_entry.AccessEntry, error) {
	var models []AccessEntryModel
	if err := r.db.WithContext(ctx).Where(where, args...).Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	entries := make([]access_entry.AccessEntry, len(models))
	ptrs := make([]*access_entry.AccessEntry, len(models))
	for i := range models {
		entries[i] = *models[i].toDomain()
		if err := r.decryptPassword(&models[i], &entries[i]); err != nil {
			return nil, err
		}
		ptrs[i] = &entries[i]
	}
	if err := r.hydrateKeys(ctx, accountID, ptrs); err != nil {
		return nil, err
	}
	if err := r.hydrateOUs(ctx, accountID, ptrs); err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *AccessEntryRepository) AttachKey(ctx context.Context, accountID, entryID, keyID uuid.UUID) error {
	// Scope the entry to the account so a caller cannot attach to another
	// account's entry, then upsert the join row idempotently.
	var count int64
	if err := r.db.WithContext(ctx).Model(&AccessEntryModel{}).
		Where("id = ? AND account_id = ?", entryID, accountID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return app_errors.ErrAccessEntryNotFound
	}
	row := &AccessEntryKeyModel{AccessEntryID: entryID, SSHKeyID: keyID, AccountID: accountID}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return err
	}
	return r.touchUpdatedAt(ctx, accountID, entryID)
}

func (r *AccessEntryRepository) DetachKey(ctx context.Context, accountID, entryID, keyID uuid.UUID) error {
	if err := r.db.WithContext(ctx).
		Where("access_entry_id = ? AND ssh_key_id = ? AND account_id = ?", entryID, keyID, accountID).
		Delete(&AccessEntryKeyModel{}).Error; err != nil {
		return err
	}
	return r.touchUpdatedAt(ctx, accountID, entryID)
}

// touchUpdatedAt bumps an entry's updated_at, so attaching or detaching a key —
// which changes what the entry grants — shows in the "Updated" column.
func (r *AccessEntryRepository) touchUpdatedAt(ctx context.Context, accountID, entryID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&AccessEntryModel{}).
		Where("id = ? AND account_id = ?", entryID, accountID).
		UpdateColumn("updated_at", time.Now()).Error
}

// DeleteByOUID detaches every entry from a deleted unit. With multi-OU
// membership the entry itself is NOT deleted: a user living in two units must
// survive one unit's removal, and a single-unit user degrades to unassigned
// (applies nowhere) instead of silently losing its keys and password.
func (r *AccessEntryRepository) DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Delete(&AccessEntryOUModel{}).Error
}

// DetachNode clears the node target of every entry scoped to a deleted node.
// Same shape and same reasoning as DeleteByOUID: the entry carries a password
// and a key set someone authored, so it degrades to unassigned (applies to no
// node, still listed under scope=unassigned) rather than being destroyed. It
// also has to stop applying, because node ids are reused — a tombstone restore
// brings the node back under its original id, and an entry still pointing at it
// would hand the device credentials the delete was supposed to have taken away.
func (r *AccessEntryRepository) DetachNode(ctx context.Context, accountID, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&AccessEntryModel{}).
		Where("account_id = ? AND node_id = ?", accountID, nodeID).
		UpdateColumns(map[string]interface{}{"node_id": nil, "updated_at": time.Now()}).Error
}

// CountByOU returns, per OU, how many access entries are scoped directly to it,
// plus the unassigned bucket (unattached, not account-wide — applies nowhere),
// the account-default bucket (unattached, account-wide — applies fleet-wide) and
// the account total. Node-scoped entries are not counted against an OU here —
// the rail groups by OU, and a node override is edited from the node, not the
// OU. Mirrors the secrets OU-count facet.
func (r *AccessEntryRepository) CountByOU(ctx context.Context, accountID uuid.UUID) (counts map[uuid.UUID]repository.AccessEntryCounts, unassigned, accountWide repository.AccessEntryCounts, total int64, err error) {
	fail := func(e error) (map[uuid.UUID]repository.AccessEntryCounts, repository.AccessEntryCounts, repository.AccessEntryCounts, int64, error) {
		return nil, repository.AccessEntryCounts{}, repository.AccessEntryCounts{}, 0, e
	}
	add := func(c *repository.AccessEntryCounts, typ string, n int64) {
		if typ == string(access_entry.TypeCA) {
			c.CAs += n
		} else {
			c.Users += n
		}
	}
	// Explicit column tags everywhere: GORM's default naming maps the OUID
	// field to a column named "ouid", NOT "ou_id" (consecutive capitals
	// collapse), so an untagged Scan silently leaves it nil.
	type ouRow struct {
		OUID  uuid.UUID `gorm:"column:ou_id"`
		Type  string    `gorm:"column:type"`
		Count int64     `gorm:"column:count"`
	}
	var ouRows []ouRow
	// Per-OU counts come from the join table: an entry living in N units counts
	// once in each unit's badge (that IS its visibility there).
	if err := r.db.WithContext(ctx).
		Table("access_entry_ous aeo").
		Select("aeo.ou_id, ae.type, count(*) as count").
		Joins("JOIN access_entries ae ON ae.id = aeo.access_entry_id").
		Where("aeo.account_id = ?", accountID).
		Group("aeo.ou_id, ae.type").
		Scan(&ouRows).Error; err != nil {
		return fail(err)
	}
	counts = make(map[uuid.UUID]repository.AccessEntryCounts)
	for _, rw := range ouRows {
		c := counts[rw.OUID]
		add(&c, rw.Type, rw.Count)
		counts[rw.OUID] = c
	}

	// Unattached buckets: entries with no node and no unit memberships,
	// split into unassigned (applies nowhere) vs account-wide (fleet default).
	type bucketRow struct {
		AccountWide bool   `gorm:"column:account_wide"`
		Type        string `gorm:"column:type"`
		Count       int64  `gorm:"column:count"`
	}
	var bucketRows []bucketRow
	if err := r.db.WithContext(ctx).
		Model(&AccessEntryModel{}).
		Select("account_wide, type, count(*) as count").
		Where("account_id = ? AND node_id IS NULL AND NOT "+ouMembership+" IS NOT NULL)", accountID).
		Group("account_wide, type").
		Scan(&bucketRows).Error; err != nil {
		return fail(err)
	}
	for _, rw := range bucketRows {
		if rw.AccountWide {
			add(&accountWide, rw.Type, rw.Count)
		} else {
			add(&unassigned, rw.Type, rw.Count)
		}
	}

	// Total counts each non-node entry once, however many units it lives in.
	if err := r.db.WithContext(ctx).
		Model(&AccessEntryModel{}).
		Where("account_id = ? AND node_id IS NULL", accountID).
		Count(&total).Error; err != nil {
		return fail(err)
	}
	return counts, unassigned, accountWide, total, nil
}

func (r *AccessEntryRepository) DeleteKeyReferences(ctx context.Context, accountID, keyID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("ssh_key_id = ? AND account_id = ?", keyID, accountID).
		Delete(&AccessEntryKeyModel{}).Error
}

// --- helpers -------------------------------------------------------------

func (r *AccessEntryRepository) setEncryptedPassword(model *AccessEntryModel, e *access_entry.AccessEntry) error {
	if e.Password == nil || *e.Password == "" {
		model.PasswordEncrypted = nil
		return nil
	}
	enc, err := r.encryptor.Encrypt(*e.Password)
	if err != nil {
		return err
	}
	model.PasswordEncrypted = &enc
	return nil
}

func (r *AccessEntryRepository) decryptPassword(model *AccessEntryModel, e *access_entry.AccessEntry) error {
	if model.PasswordEncrypted == nil || *model.PasswordEncrypted == "" {
		e.Password = nil
		return nil
	}
	plain, err := r.encryptor.Decrypt(*model.PasswordEncrypted)
	if err != nil {
		return err
	}
	e.Password = &plain
	return nil
}

// insertOUs writes join rows for a set of unit ids (idempotently).
func (r *AccessEntryRepository) insertOUs(tx *gorm.DB, accountID, entryID uuid.UUID, ouIDs []uuid.UUID) error {
	if len(ouIDs) == 0 {
		return nil
	}
	rows := make([]AccessEntryOUModel, 0, len(ouIDs))
	for _, ou := range ouIDs {
		rows = append(rows, AccessEntryOUModel{AccessEntryID: entryID, OUID: ou, AccountID: accountID})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// hydrateOUs loads the unit memberships for the given entries in one query.
func (r *AccessEntryRepository) hydrateOUs(ctx context.Context, accountID uuid.UUID, entries []*access_entry.AccessEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(entries))
	byID := make(map[uuid.UUID]*access_entry.AccessEntry, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
		byID[e.ID] = e
		e.OUIDs = nil
	}
	var rows []AccessEntryOUModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND access_entry_id IN ?", accountID, ids).
		Order("ou_id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if e, ok := byID[row.AccessEntryID]; ok {
			e.OUIDs = append(e.OUIDs, row.OUID)
		}
	}
	return nil
}

// insertKeys writes join rows for a set of key ids (idempotently).
func (r *AccessEntryRepository) insertKeys(tx *gorm.DB, accountID, entryID uuid.UUID, keyIDs []uuid.UUID) error {
	if len(keyIDs) == 0 {
		return nil
	}
	rows := make([]AccessEntryKeyModel, 0, len(keyIDs))
	for _, k := range keyIDs {
		rows = append(rows, AccessEntryKeyModel{AccessEntryID: entryID, SSHKeyID: k, AccountID: accountID})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// hydrateKeys loads the join rows for the given entries and fills their KeyIDs
// in one query (keyed by entry id) to avoid N+1.
func (r *AccessEntryRepository) hydrateKeys(ctx context.Context, accountID uuid.UUID, entries []*access_entry.AccessEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(entries))
	byID := make(map[uuid.UUID]*access_entry.AccessEntry, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
		byID[e.ID] = e
		e.KeyIDs = nil
	}
	var rows []AccessEntryKeyModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND access_entry_id IN ?", accountID, ids).
		Order("ssh_key_id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if e, ok := byID[row.AccessEntryID]; ok {
			e.KeyIDs = append(e.KeyIDs, row.SSHKeyID)
		}
	}
	return nil
}
