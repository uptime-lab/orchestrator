package sql

import (
	"context"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.SecretAttachmentRepository = (*SecretAttachmentRepository)(nil)

// SecretAttachmentRepository persists secret-to-target attachments.
type SecretAttachmentRepository struct {
	db *gorm.DB
}

func NewSecretAttachmentRepository(db *gorm.DB) *SecretAttachmentRepository {
	return &SecretAttachmentRepository{db: db}
}

func (r *SecretAttachmentRepository) Setup() error {
	if err := r.db.AutoMigrate(&SecretAttachmentModel{}); err != nil {
		return err
	}
	// secret_id predates tag subjects and was created NOT NULL; tag
	// attachments carry a null secret_id (idempotent to re-run).
	if err := r.db.Exec(`ALTER TABLE secret_attachments ALTER COLUMN secret_id DROP NOT NULL`).Error; err != nil {
		return err
	}
	if err := r.migrateGroupsToTags(); err != nil {
		return err
	}
	if err := r.dedupeAttachments(); err != nil {
		return err
	}
	// Partial unique indexes (nullable subject/target columns), created with
	// raw SQL since GORM tags cannot express the WHERE clause.
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_secret_attachments_secret_ou ON secret_attachments (secret_id, ou_id) WHERE secret_id IS NOT NULL AND ou_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_secret_attachments_secret_node ON secret_attachments (secret_id, node_id) WHERE secret_id IS NOT NULL AND node_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_secret_attachments_tag_ou ON secret_attachments (account_id, tag, ou_id) WHERE tag <> '' AND ou_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_secret_attachments_tag_node ON secret_attachments (account_id, tag, node_id) WHERE tag <> '' AND node_id IS NOT NULL`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// dedupeAttachments removes rows the partial unique indexes above would reject,
// keeping the first of each group.
//
// The partition MUST include `tag`, and leaving it out is a data-loss bug.
// An attachment's subject is a secret XOR a tag, so a tag
// attachment carries a NULL secret_id — and Postgres puts all NULLs in ONE
// window partition. Without `tag`, every tag attachment on a target grouped
// together whatever tag it named, and everything after the first was deleted.
// This is not a one-time migration: it runs on every backend start, so an OU
// with three tags attached would have kept one across a restart and silently
// stopped delivering the secrets the other two carried.
//
// It runs AFTER AutoMigrate and after the group→tag migration, which is the
// second half of the fix: before AutoMigrate the `tag` column does not exist on
// a pre-tag schema, so the query would fail and take the whole boot with it
// (every Setup() is fatal in main.go); and the group migration writes tags of
// its own, which can collide with each other.
//
// The partition is deliberately MORE granular than the four indexes, so it can
// only ever delete a row one of them would have rejected.
func (r *SecretAttachmentRepository) dedupeAttachments() error {
	return r.db.Exec(`DELETE FROM secret_attachments WHERE id IN (
		SELECT id FROM (
			SELECT id, row_number() OVER (
				PARTITION BY account_id, secret_id, tag, ou_id, node_id ORDER BY id
			) AS rn
			FROM secret_attachments
		) d WHERE d.rn > 1)`).Error
}

// migrateGroupsToTags converts the retired secret-group model into tag
// attachments, preserving delivery exactly: every member secret gains a tag
// derived from the group name, every group attachment becomes a tag
// attachment, then the group column/table are dropped. Idempotent: it only
// runs while the legacy schema still exists.
func (r *SecretAttachmentRepository) migrateGroupsToTags() error {
	migrator := r.db.Migrator()
	if !migrator.HasTable("secret_groups") {
		return nil
	}

	var groups []struct {
		ID   uuid.UUID
		Name string
	}
	if err := r.db.Raw(`SELECT id, name FROM secret_groups`).Scan(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		tag := slugifyGroupName(group.Name)
		if tag == "" {
			tag = "group-" + group.ID.String()[:8]
		}
		// Tag every member secret (skip when already tagged; respect the cap
		// loosely — migration must not fail on over-tagged secrets).
		err := r.db.Exec(`UPDATE secrets SET tags = array_append(COALESCE(tags, '{}'), ?::text)
			WHERE group_id = ? AND NOT (COALESCE(tags, '{}') @> ARRAY[?::text])`,
			tag, group.ID, tag).Error
		if err != nil {
			return err
		}
		// Convert the group's attachments into tag attachments.
		err = r.db.Exec(`UPDATE secret_attachments SET tag = ?, group_id = NULL WHERE group_id = ?`,
			tag, group.ID).Error
		if err != nil {
			return err
		}
	}

	// Drop the legacy schema.
	for _, stmt := range []string{
		`ALTER TABLE secret_attachments DROP COLUMN IF EXISTS group_id`,
		`ALTER TABLE secrets DROP COLUMN IF EXISTS group_id`,
		`DROP TABLE IF EXISTS secret_groups`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// slugifyGroupName maps a free-form group name onto the tag charset.
func slugifyGroupName(name string) string {
	out := make([]rune, 0, len(name))
	for _, c := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	slug := strings.Trim(string(out), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug
}

// Attach persists an attachment. It is idempotent: attaching an already
// attached (secret, target) pair succeeds and reports the existing row's ID.
func (r *SecretAttachmentRepository) Attach(ctx context.Context, att *secret.SecretAttachment) error {
	model := &SecretAttachmentModel{}
	model.FromDomain(att)
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(model)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// Conflict — load the existing attachment so callers see its real ID.
	var existing SecretAttachmentModel
	q := r.db.WithContext(ctx).Where("account_id = ?", att.AccountID)
	if att.SecretID != nil {
		q = q.Where("secret_id = ?", *att.SecretID)
	} else {
		q = q.Where("tag = ?", att.Tag)
	}
	if att.OUID != nil {
		q = q.Where("ou_id = ?", *att.OUID)
	} else {
		q = q.Where("node_id = ?", *att.NodeID)
	}
	if err := q.First(&existing).Error; err != nil {
		return err
	}
	*att = *existing.ToDomain()
	return nil
}

func (r *SecretAttachmentRepository) Detach(ctx context.Context, accountID, attachmentID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", attachmentID, accountID).
		Delete(&SecretAttachmentModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrSecretAttachmentNotFound
	}
	return nil
}

func (r *SecretAttachmentRepository) ListBySecret(ctx context.Context, accountID, secretID uuid.UUID) ([]secret.SecretAttachment, error) {
	return r.query(ctx, "account_id = ? AND secret_id = ?", accountID, secretID)
}

func (r *SecretAttachmentRepository) ListByTag(ctx context.Context, accountID uuid.UUID, tag string) ([]secret.SecretAttachment, error) {
	return r.query(ctx, "account_id = ? AND tag = ?", accountID, tag)
}

func (r *SecretAttachmentRepository) DeliveryCountsByOU(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int, error) {
	// Resolve every attachment to (target OU, delivered secret): node targets
	// roll up to the node's OU; tag subjects expand to every carrying secret.
	var rows []struct {
		TargetOU uuid.UUID
		Count    int
	}
	err := r.db.WithContext(ctx).
		Raw(`SELECT target_ou, count(DISTINCT sid) AS count FROM (
			SELECT COALESCE(a.ou_id, n.ou_id) AS target_ou,
			       COALESCE(a.secret_id, s.id) AS sid
			FROM secret_attachments a
			LEFT JOIN nodes n ON n.id = a.node_id AND n.account_id = a.account_id
			LEFT JOIN secrets s ON a.tag <> '' AND s.tags @> ARRAY[a.tag]::text[] AND s.account_id = a.account_id
			WHERE a.account_id = ?
		) t
		WHERE target_ou IS NOT NULL AND sid IS NOT NULL
		GROUP BY target_ou`, accountID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		counts[row.TargetOU] = row.Count
	}
	return counts, nil
}

func (r *SecretAttachmentRepository) CountsBySubject(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]secret.AttachmentCounts, map[string]secret.AttachmentCounts, error) {
	var rows []struct {
		SecretID *uuid.UUID
		Tag      string
		OUs      int
		Nodes    int
	}
	err := r.db.WithContext(ctx).
		Model(&SecretAttachmentModel{}).
		Select("secret_id, tag, count(ou_id) as o_us, count(node_id) as nodes").
		Where("account_id = ?", accountID).
		Group("secret_id, tag").
		Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	secretCounts := make(map[uuid.UUID]secret.AttachmentCounts)
	tagCounts := make(map[string]secret.AttachmentCounts)
	for _, row := range rows {
		counts := secret.AttachmentCounts{OUs: row.OUs, Nodes: row.Nodes}
		switch {
		case row.SecretID != nil:
			secretCounts[*row.SecretID] = counts
		case row.Tag != "":
			tagCounts[row.Tag] = counts
		}
	}
	return secretCounts, tagCounts, nil
}

// DeleteBySecret removes every attachment of a secret (used on secret delete).
func (r *SecretAttachmentRepository) DeleteBySecret(ctx context.Context, accountID, secretID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND secret_id = ?", accountID, secretID).
		Delete(&SecretAttachmentModel{}).Error
}

// DeleteByTag removes every attachment of a tag subject.
func (r *SecretAttachmentRepository) DeleteByTag(ctx context.Context, accountID uuid.UUID, tag string) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND tag = ?", accountID, tag).
		Delete(&SecretAttachmentModel{}).Error
}

// DeleteByOUID removes every attachment targeting an OU (OU deletion cleanup).
func (r *SecretAttachmentRepository) DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Delete(&SecretAttachmentModel{}).Error
}

// DeleteByNodeID removes every attachment targeting a node (node deletion
// cleanup). Only the link goes: the secret behind it is an account-level object
// and stays, which for an attribute-backing secret (attr:<nodeID>:<key>) leaves
// it unattached — findable and removable through the list's unattached filter,
// the only path to it once the node is gone.
func (r *SecretAttachmentRepository) DeleteByNodeID(ctx context.Context, accountID, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND node_id = ?", accountID, nodeID).
		Delete(&SecretAttachmentModel{}).Error
}

func (r *SecretAttachmentRepository) ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]secret.SecretAttachment, error) {
	return r.query(ctx, "account_id = ? AND ou_id = ?", accountID, ouID)
}

func (r *SecretAttachmentRepository) ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]secret.SecretAttachment, error) {
	return r.query(ctx, "account_id = ? AND node_id = ?", accountID, nodeID)
}

func (r *SecretAttachmentRepository) ListByOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) ([]secret.SecretAttachment, error) {
	if len(ouIDs) == 0 {
		return nil, nil
	}
	return r.query(ctx, "account_id = ? AND ou_id IN ?", accountID, ouIDs)
}

// query runs a scoped SELECT and maps rows to domain attachments.
func (r *SecretAttachmentRepository) query(ctx context.Context, where string, args ...interface{}) ([]secret.SecretAttachment, error) {
	var models []SecretAttachmentModel
	result := r.db.WithContext(ctx).
		Where(where, args...).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	atts := make([]secret.SecretAttachment, len(models))
	for i := range models {
		atts[i] = *models[i].ToDomain()
	}
	return atts, nil
}
