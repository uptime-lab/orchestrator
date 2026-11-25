package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.NodeFileRepository = (*NodeFileRepository)(nil)

// NodeFileRepository is the implementation of the node file repository using SQL
type NodeFileRepository struct {
	db  *gorm.DB
	enc fileEncryptor
}

// fileEncryptor is the crypto surface tracked-file content needs. Tracked files
// are arbitrary device files — netplan configs with a PSK, application .env —
// so they are encrypted at rest like secrets rather than sitting in the dump in
// the clear (a development install also logs every SQL statement with its values).
type fileEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// autoMigrate creates the necessary database tables
func (r *NodeFileRepository) autoMigrate() error {
	return r.db.AutoMigrate(&NodeFileModel{})
}

// Setup initializes the repository and creates tables if needed.
func (r *NodeFileRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	return r.backfillAccountID()
}

// backfillAccountID fills the denormalized account for rows written before the
// column existed. Idempotent, and it deliberately leaves rows whose node is
// gone alone: node_files rows are orphaned today when a node is deleted, and
// inventing an owner for them would be worse than leaving them unattributed.
func (r *NodeFileRepository) backfillAccountID() error {
	return r.db.Exec(`
		UPDATE node_files SET account_id = n.account_id
		FROM nodes n
		WHERE n.id = node_files.node_id AND node_files.account_id IS NULL`).Error
}

// versionUniqueIndexName is the index CreateNextVersion relies on to turn a
// version collision into an error instead of a silent duplicate.
const versionUniqueIndexName = "idx_node_files_node_path_version"

// EnsureVersionUniqueIndex renumbers any tied versions and then creates the
// unique index on (node_id, path, version).
//
// It is NOT a struct tag, and it is NOT called from Setup(). AutoMigrate would
// try to create the index directly, and on a live table that already holds a
// tie the creation fails — which, from Setup(), is a fatal error on every
// start: a crash-loop over a hygiene migration, on an install whose data is
// perfectly serviceable. So the duplicates are resolved first, and the caller
// treats a failure as "the guard is not in place yet", not as "do not boot".
//
// Returns how many rows were renumbered.
func (r *NodeFileRepository) EnsureVersionUniqueIndex() (int, error) {
	renumbered, err := r.dedupeVersions()
	if err != nil {
		return 0, fmt.Errorf("resolving duplicate file versions: %w", err)
	}
	if err := r.db.Exec(fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS %s ON node_files (node_id, path, version)`,
		versionUniqueIndexName)).Error; err != nil {
		return renumbered, fmt.Errorf("creating %s: %w", versionUniqueIndexName, err)
	}
	return renumbered, nil
}

// dedupeVersions gives every row of a tie its own version number.
//
// The oldest row of each tie KEEPS the contested number and the others are
// moved above the path's current maximum, rather than renumbering the path
// densely from 1. Two reasons. Densely renumbering rewrites version numbers the
// operator has already seen for rows that were never in conflict — history has
// gaps by design, because retention prunes the oldest versions. And a tie is by
// definition two writes that landed at the same instant, so both are "the
// newest"; moving the extras to the top preserves that, and GetLatest (version
// DESC) then returns the more recent content rather than an arbitrary one.
//
// Nothing durable pins a version number, with one narrow exception: an
// in-flight write_file carries its version in the command payload and the
// settle looks the row up by (node, path, version). A backend restarting in the
// exact second between dispatch and settle, for a path that also holds a tie,
// would settle the wrong row. That is a smaller and rarer failure than leaving
// the duplicates in place, and it cannot happen at all once the index exists.
func (r *NodeFileRepository) dedupeVersions() (int, error) {
	result := r.db.Exec(`
		WITH tied AS (
			SELECT id, node_id, path, version,
				row_number() OVER (PARTITION BY node_id, path, version ORDER BY created_at, id) AS seq
			FROM node_files
		),
		extra AS (SELECT * FROM tied WHERE seq > 1),
		peak AS (SELECT node_id, path, MAX(version) AS top FROM node_files GROUP BY node_id, path),
		moved AS (
			SELECT e.id,
				p.top + row_number() OVER (PARTITION BY e.node_id, e.path ORDER BY e.version, e.id) AS new_version
			FROM extra e
			JOIN peak p ON p.node_id = e.node_id AND p.path = e.path
		)
		UPDATE node_files f
		SET version = m.new_version, updated_at = now()
		FROM moved m
		WHERE f.id = m.id`)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// RepairMislabeledContent re-encrypts rows that carry the encrypted marker but
// hold plain text, and reports how many it fixed.
//
// Those rows are the residue of a rollback: an old backend served the at-rest
// envelope as file content, and any save made in that state wrote plain text
// under a marker the old model did not know about. The read path already
// tolerates them (see mislabeledPlaintext), so this is not what makes the file
// readable again — it is what stops the anomaly from being permanent, and what
// puts a number on how far the rollback spread in the logs.
//
// Rows that look like the envelope are never touched: those are ciphertext this
// backend cannot decrypt, which is a key problem, and re-encrypting them would
// wrap the unreadable bytes in a second envelope and destroy the original for
// good. Same rule as the secrets rewrap, learned the same way.
func (r *NodeFileRepository) RepairMislabeledContent() (int, error) {
	if r.enc == nil {
		return 0, nil
	}
	type row struct {
		ID      uuid.UUID
		Content string
	}
	repaired := 0
	// Paged by id rather than by a moving predicate: a repaired row still
	// matches `content_enc = 1`, so an offsetless re-query would loop forever.
	var lastID uuid.UUID
	for {
		var rows []row
		if err := r.db.Table("node_files").
			Select("id, content").
			Where("content_enc = 1 AND content <> '' AND id > ?", lastID).
			Order("id").
			Limit(contentBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return repaired, err
		}
		if len(rows) == 0 {
			return repaired, nil
		}
		for _, rw := range rows {
			lastID = rw.ID
			if node_file.LooksLikeStoredCiphertext(rw.Content) {
				continue
			}
			ciphertext, err := r.enc.Encrypt(rw.Content)
			if err != nil {
				return repaired, fmt.Errorf("re-encrypting node_files.%s: %w", rw.ID, err)
			}
			if err := r.db.Table("node_files").
				Where("id = ? AND content = ?", rw.ID, rw.Content).
				Updates(map[string]interface{}{"content": ciphertext, "content_enc": 1}).Error; err != nil {
				return repaired, err
			}
			repaired++
		}
	}
}

// BackfillContentEncryption encrypts the file contents written before the
// column existed. Separate from Setup() and called non-fatally: a failure here
// means "some rows are still plaintext", which the platform serves correctly,
// whereas aborting startup would crash-loop a live install over a hygiene task.
// Idempotent — it only touches rows still marked plaintext.
func (r *NodeFileRepository) BackfillContentEncryption() (int, error) {
	if r.enc == nil {
		return 0, nil
	}
	type row struct {
		ID      uuid.UUID
		Content string
	}
	converted := 0
	for {
		var rows []row
		if err := r.db.Table("node_files").
			Select("id, content").
			Where("content_enc = 0 AND content <> ''").
			Limit(contentBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, rw := range rows {
			ciphertext, err := r.enc.Encrypt(rw.Content)
			if err != nil {
				return converted, fmt.Errorf("encrypting node_files.%s: %w", rw.ID, err)
			}
			if err := r.db.Table("node_files").
				Where("id = ? AND content_enc = 0", rw.ID).
				Updates(map[string]interface{}{"content": ciphertext, "content_enc": 1}).Error; err != nil {
				return converted, err
			}
			converted++
		}
	}
}

// NewNodeFileRepository creates a new node file repository
func NewNodeFileRepository(db *gorm.DB, enc fileEncryptor) *NodeFileRepository {
	return &NodeFileRepository{db: db, enc: enc}
}

// encryptContent prepares a domain file for storage.
func (r *NodeFileRepository) encryptContent(model *NodeFileModel, plaintext string) error {
	if r.enc == nil || plaintext == "" {
		model.Content = plaintext
		model.ContentEnc = 0
		return nil
	}
	ciphertext, err := r.enc.Encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("failed to encrypt file content: %w", err)
	}
	model.Content = ciphertext
	model.ContentEnc = 1
	return nil
}

// decryptContent reverses it. A failure is an ERROR, deliberately, not an empty
// string: an empty editor invites an Apply that writes an empty file onto the
// device, which is exactly the failure this feature exists to prevent.
//
// The one exception is a row whose MARKER is lying — see mislabeledPlaintext.
func (r *NodeFileRepository) decryptContent(model *NodeFileModel) (string, error) {
	if model.ContentEnc == 0 || model.Content == "" {
		return model.Content, nil
	}
	if r.enc == nil {
		if mislabeledPlaintext(model) {
			return model.Content, nil
		}
		return "", node_file.ErrContentUnreadable
	}
	plaintext, err := r.enc.Decrypt(model.Content)
	if err != nil {
		if mislabeledPlaintext(model) {
			return model.Content, nil
		}
		return "", fmt.Errorf("%w: %v", node_file.ErrContentUnreadable, err)
	}
	return plaintext, nil
}

// mislabeledPlaintext reports whether a row marked encrypted actually holds a
// file's plain text.
//
// This is the rollback wound. Deploying a backend without the encryption code
// over already-encrypted rows makes the editor render "v1:…" ciphertext as the
// file's content. If the operator then SAVES in that
// state, the old backend writes their plain text into `content` and never
// touches `content_enc`, because its model has no such column. The row is now
// plaintext wearing the encrypted marker, and a backend with the decryption
// code back in place refuses to serve it — permanently, for a file whose
// content is sitting right there in the clear.
//
// The discriminator is the shape of the stored value, and it is sound in both
// directions. A genuine decryption failure — the wrong key, a rotated key not
// listed as a fallback — still has envelope-shaped ciphertext, so it keeps
// erroring, which is what an operator in the middle of a key rotation needs to
// see. Only a value that is not the envelope at all can have come from a writer
// that did not encrypt.
func mislabeledPlaintext(model *NodeFileModel) bool {
	return model.ContentEnc == 1 && !node_file.LooksLikeStoredCiphertext(model.Content)
}

// toDomain converts and decrypts in one step so no read path can forget.
func (r *NodeFileRepository) toDomain(model *NodeFileModel) (*node_file.NodeFile, error) {
	file := model.ToDomain()
	content, err := r.decryptContent(model)
	if err != nil {
		return nil, err
	}
	file.Content = content
	return file, nil
}

// ListLatestVersionMeta returns the newest version's metadata for every path a
// node has history for, in one query and without touching content.
//
// The Files list used to call GetLatest per tracked row: up to fifty queries,
// each decrypting a whole file, for four scalar fields that are then rendered
// and the content discarded. Worse, a row that failed to decrypt was silently
// dropped to nil and rendered as "never read" — the list said a file had never
// been read off the device when the platform was holding versions of it.
// Reading no content at all removes both problems: nothing to decrypt, so
// nothing to fail, and the metadata is reported whatever the content's state.
func (r *NodeFileRepository) ListLatestVersionMeta(ctx context.Context, nodeID uuid.UUID) ([]node_file.VersionMeta, error) {
	var rows []struct {
		Path      string
		Version   int
		Status    string
		Hash      string
		UpdatedAt time.Time
	}
	err := r.db.WithContext(ctx).
		Table("node_files").
		Select("path, version, status, hash, updated_at").
		// The subquery alias is "newest", not "inner": INNER is a reserved word
		// in Postgres and an unquoted table alias by that name is a syntax error.
		Where(`node_id = ? AND version = (
			SELECT MAX(newest.version) FROM node_files newest
			WHERE newest.node_id = node_files.node_id AND newest.path = node_files.path
		)`, nodeID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]node_file.VersionMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, node_file.VersionMeta{
			Path:      row.Path,
			Version:   row.Version,
			Status:    row.Status,
			Hash:      row.Hash,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

// versionAllocationAttempts bounds the retry on a version collision. A retry is
// one losing racer re-reading a maximum that has already moved, so a handful is
// generous; the bound exists so a genuinely broken index cannot spin forever.
const versionAllocationAttempts = 5

// CreateNextVersion inserts a file version and allocates its number in the same
// statement, writing the allocated number back into file.Version.
//
// The number is computed by the INSERT's own subquery rather than by a prior
// read, and the unique index on (node_id, path, version) is what makes that
// safe: under snapshot isolation two concurrent inserts can still read the same
// maximum, and the index turns that into a unique violation instead of a silent
// tie. The loser retries and re-reads the now-higher maximum. Without the index
// the collision is invisible — two rows claim to be version 4, GetLatest keeps
// whichever the planner returns first, and an Apply then writes content the
// operator never saw to the device.
//
// account_id is denormalized from the node in the same statement rather than
// threaded through the callers. The column is a second tenancy layer — every
// read is already scoped through the node — and the point of a second layer is
// that it cannot be forgotten: a caller that neglected to pass an account id
// would silently write an unattributable row, and the gRPC file-changed path
// has only a node id to begin with. Selecting it from nodes also means a
// version row for a node that no longer exists is not written at all, which is
// the correct outcome for one.
func (r *NodeFileRepository) CreateNextVersion(ctx context.Context, file *node_file.NodeFile) error {
	model := &NodeFileModel{}
	model.FromDomain(file)
	if err := r.encryptContent(model, file.Content); err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < versionAllocationAttempts; attempt++ {
		var allocated int
		err := r.db.WithContext(ctx).Raw(`
			INSERT INTO node_files
				(id, account_id, node_id, path, content, hash, content_enc, version, status, error_message, created_at, updated_at)
			SELECT ?, n.account_id, ?, ?, ?, ?, ?,
				COALESCE((SELECT MAX(v.version) FROM node_files v WHERE v.node_id = ? AND v.path = ?), 0) + 1,
				?, ?, now(), now()
			FROM nodes n WHERE n.id = ?
			RETURNING version`,
			model.ID, model.NodeID, model.Path, model.Content, model.Hash, model.ContentEnc,
			model.NodeID, model.Path,
			model.Status, model.ErrorMessage, model.NodeID).Scan(&allocated).Error
		if err != nil {
			if isUniqueViolation(err) {
				lastErr = err
				continue
			}
			return err
		}
		if allocated == 0 {
			// The INSERT ... SELECT matched no node, so nothing was written.
			return fmt.Errorf("cannot store a file version for unknown node %s", file.NodeID)
		}
		file.Version = allocated
		return nil
	}
	return fmt.Errorf("failed to allocate a version for %s after %d attempts: %w",
		file.Path, versionAllocationAttempts, lastErr)
}

// Update updates an existing file version
func (r *NodeFileRepository) Update(ctx context.Context, file *node_file.NodeFile) error {
	model := &NodeFileModel{}
	model.FromDomain(file)
	if err := r.encryptContent(model, file.Content); err != nil {
		return err
	}

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	// Omit account_id: the domain object has no such field, so FromDomain leaves
	// the model's pointer nil and a full-row write — which writes EVERY column —
	// would NULL the denormalized owner on the first status update after the
	// insert. That happens microseconds later on the read path (create the
	// version, then mark it applied), so the column emptied itself almost
	// immediately and the per-account storage ceiling would have counted nothing.
	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ?", file.ID).
		Omit("account_id"), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeNotFound // Reuse error, or create specific one
	}

	return nil
}

// GetByNodeIDAndPath retrieves all versions of a file for a node and path
func (r *NodeFileRepository) GetByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) ([]node_file.NodeFile, error) {
	var models []NodeFileModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND path = ?", nodeID, path).
		Order("version DESC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	files := make([]node_file.NodeFile, len(models))
	for i, model := range models {
		domainFile, derr := r.toDomain(&model)
		if derr != nil {
			return nil, derr
		}
		files[i] = *domainFile
	}

	return files, nil
}

// GetLatestByNodeIDAndPath retrieves the latest version of a file for a node and path
func (r *NodeFileRepository) GetLatestByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.NodeFile, error) {
	var model NodeFileModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND path = ?", nodeID, path).
		Order("version DESC").
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound // Reuse error, or create specific one
		}
		return nil, result.Error
	}

	return r.toDomain(&model)
}

// GetByNodeIDPathAndVersion retrieves a specific version of a file
func (r *NodeFileRepository) GetByNodeIDPathAndVersion(ctx context.Context, nodeID uuid.UUID, path string, version int) (*node_file.NodeFile, error) {
	var model NodeFileModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND path = ? AND version = ?", nodeID, path, version).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound // Reuse error, or create specific one
		}
		return nil, result.Error
	}

	return r.toDomain(&model)
}

// GetAllPathsByNodeID retrieves all unique paths for a node (returns latest version of each)
func (r *NodeFileRepository) GetAllPathsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.NodeFile, error) {
	var models []NodeFileModel

	// Use a subquery to get the latest version of each path
	subquery := r.db.WithContext(ctx).
		Model(&NodeFileModel{}).
		Select("path, MAX(version) as max_version").
		Where("node_id = ?", nodeID).
		Group("path")

	result := r.db.WithContext(ctx).
		Table("node_files as nf").
		Joins("INNER JOIN (?) as sq ON nf.path = sq.path AND nf.version = sq.max_version", subquery).
		Where("nf.node_id = ?", nodeID).
		Order("nf.path ASC").
		Find(&models)

	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	files := make([]node_file.NodeFile, len(models))
	for i, model := range models {
		domainFile, derr := r.toDomain(&model)
		if derr != nil {
			return nil, derr
		}
		files[i] = *domainFile
	}

	return files, nil
}

// GetByID retrieves a file version by its ID
func (r *NodeFileRepository) GetByID(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error) {
	var model NodeFileModel
	result := r.db.WithContext(ctx).
		Where("id = ?", fileID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeNotFound // Reuse error, or create specific one
		}
		return nil, result.Error
	}

	return r.toDomain(&model)
}

// DeleteByNodeIDAndPath removes every version of one path.
func (r *NodeFileRepository) DeleteByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) error {
	return r.db.WithContext(ctx).
		Where("node_id = ? AND path = ?", nodeID, path).
		Delete(&NodeFileModel{}).Error
}

// DeleteByNodeID removes every file version of a node. Node deletion used to
// leave these rows behind, so a node id could be reused with someone else's
// file history attached to it.
func (r *NodeFileRepository) DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("node_id = ?", nodeID).
		Delete(&NodeFileModel{}).Error
}

// PruneVersions keeps the newest `keep` versions of one path and deletes the
// rest. Each row holds the whole file, so an unbounded history is a table that
// grows with every save.
func (r *NodeFileRepository) PruneVersions(ctx context.Context, nodeID uuid.UUID, path string, keep int) error {
	if keep <= 0 {
		return nil
	}
	var cutoff int
	err := r.db.WithContext(ctx).
		Table("node_files").
		Select("version").
		Where("node_id = ? AND path = ?", nodeID, path).
		Order("version DESC").
		Offset(keep - 1).
		Limit(1).
		Scan(&cutoff).Error
	if err != nil || cutoff == 0 {
		return err
	}
	return r.db.WithContext(ctx).
		Where("node_id = ? AND path = ? AND version < ?", nodeID, path, cutoff).
		Delete(&NodeFileModel{}).Error
}

// SumContentBytesByNodeID totals a node's stored file content. Counted in the
// database rather than tracked in a column: a counter that drifts from reality
// is worse than a query, and this runs only on the write paths.
func (r *NodeFileRepository) SumContentBytesByNodeID(ctx context.Context, nodeID uuid.UUID) (int64, error) {
	var total *int64
	err := r.db.WithContext(ctx).
		Table("node_files").
		Select("sum(length(content))").
		Where("node_id = ?", nodeID).
		Scan(&total).Error
	if err != nil || total == nil {
		return 0, err
	}
	return *total, nil
}

// SumContentBytesByAccountID returns how many bytes of file content an account
// stores across every one of its nodes. It reads the denormalized account_id
// rather than joining nodes: the per-account ceiling is the reason that column
// exists, and a join over every version row of a large fleet is the shape this
// is meant to avoid.
func (r *NodeFileRepository) SumContentBytesByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var total *int64
	err := r.db.WithContext(ctx).
		Table("node_files").
		Select("sum(length(content))").
		Where("account_id = ?", accountID).
		Scan(&total).Error
	if err != nil || total == nil {
		return 0, err
	}
	return *total, nil
}
