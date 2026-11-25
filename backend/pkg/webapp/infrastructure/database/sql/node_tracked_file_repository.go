package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"gorm.io/gorm"
)

var _ repository.NodeTrackedFileRepository = (*NodeTrackedFileRepository)(nil)

// NodeTrackedFileRepository stores tracked files in SQL.
type NodeTrackedFileRepository struct {
	db *gorm.DB
}

// NewNodeTrackedFileRepository creates the repository.
func NewNodeTrackedFileRepository(db *gorm.DB) *NodeTrackedFileRepository {
	return &NodeTrackedFileRepository{db: db}
}

// Setup migrates the table and adopts the paths that already have version rows.
func (r *NodeTrackedFileRepository) Setup() error {
	if err := r.db.AutoMigrate(&NodeTrackedFileModel{}); err != nil {
		return err
	}
	return r.backfillFromNodeFiles()
}

// backfillFromNodeFiles creates a tracked-file row for every distinct path that
// already has version history, so nothing that was visible in the old list
// disappears on deploy. Junk left by failed commands is adopted too and shows up
// as "never read" — a migration deleting a user's rows is not its call to make.
//
// Deliberately not fatal to the caller's Setup contract beyond returning the
// error: it runs once, is idempotent, and touches nothing outside its own table.
func (r *NodeTrackedFileRepository) backfillFromNodeFiles() error {
	type row struct {
		NodeID uuid.UUID
		Path   string
	}
	var rows []row
	// Only paths whose node still exists: node_files rows are orphaned today
	// when a node is deleted, and adopting those would resurrect them in the UI.
	err := r.db.Raw(`
		SELECT DISTINCT nf.node_id, nf.path
		FROM node_files nf
		JOIN nodes n ON n.id = nf.node_id
		WHERE NOT EXISTS (
			SELECT 1 FROM node_tracked_files t
			WHERE t.node_id = nf.node_id AND t.path = nf.path
		)`).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("tracked-file backfill scan: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	for _, rw := range rows {
		// Scanned as a string, not a uuid.UUID: the driver hands back text and
		// GORM's Raw().Scan() into a [16]byte fails on it.
		var accountIDStr string
		if err := r.db.Raw(`SELECT account_id::text FROM nodes WHERE id = ?`, rw.NodeID).Scan(&accountIDStr).Error; err != nil {
			return fmt.Errorf("tracked-file backfill account lookup: %w", err)
		}
		accountID, perr := uuid.Parse(accountIDStr)
		if perr != nil {
			return fmt.Errorf("tracked-file backfill account id %q: %w", accountIDStr, perr)
		}
		source := node_file.SourceUser
		if node_file.IsPlatformPath(rw.Path) {
			source = node_file.SourcePlatform
		}
		model := &NodeTrackedFileModel{}
		model.FromDomain(node_file.NewTrackedFile(accountID, rw.NodeID, rw.Path, source, uuid.Nil))
		// ON CONFLICT DO NOTHING: two backends starting at once, or a rerun.
		if err := r.db.Exec(`
			INSERT INTO node_tracked_files
				(id, account_id, node_id, path, source, size, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, now(), now())
			ON CONFLICT (node_id, path) DO NOTHING`,
			model.ID, model.AccountID, model.NodeID, model.Path, model.Source, model.CreatedBy,
		).Error; err != nil {
			return fmt.Errorf("tracked-file backfill insert: %w", err)
		}
	}
	return nil
}

// Create adds a tracked file.
func (r *NodeTrackedFileRepository) Create(ctx context.Context, file *node_file.TrackedFile) error {
	model := &NodeTrackedFileModel{}
	model.FromDomain(file)
	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return node_file.ErrAlreadyTracked
		}
		return err
	}
	return nil
}

// TrackedFileLockClass is the advisory-lock class for per-node tracked-file
// mutations ("ORCF"). It shares the lock space with MemberLockClass, so the
// class byte is what keeps the two from colliding.
const TrackedFileLockClass = 0x4F524346

// CreateUnderLimit creates a tracked file only while the node is still under its
// file cap, and returns the existing row when the path is already tracked.
//
// The check and the insert have to be one atomic step. Read-then-create in the
// service let two concurrent track polls both see 49 files, both pass the cap
// check, and both insert — the limit was exceeded by however many polls were in
// flight, and the more a user clicked, the further past it they got. The unique
// index on (node_id, path) never caught it because the racing polls were for
// DIFFERENT paths.
//
// The advisory lock is per node and held for the transaction, so it serializes
// only the tracks of one node and is released on commit or rollback. limit <= 0
// means unlimited.
func (r *NodeTrackedFileRepository) CreateUnderLimit(
	ctx context.Context,
	file *node_file.TrackedFile,
	limit int,
) (*node_file.TrackedFile, error) {
	var out *node_file.TrackedFile
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
			TrackedFileLockClass, file.NodeID.String()).Error; err != nil {
			return err
		}

		// Re-read inside the lock: another poll may have created this very path
		// while we waited, and adopting its row is the right answer — the caller
		// asked to track the file, not to be the one who created it.
		var existing NodeTrackedFileModel
		err := tx.Where("node_id = ? AND path = ?", file.NodeID, file.Path).First(&existing).Error
		if err == nil {
			out = existing.ToDomain()
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if limit > 0 {
			var count int64
			if cerr := tx.Model(&NodeTrackedFileModel{}).
				Where("node_id = ?", file.NodeID).Count(&count).Error; cerr != nil {
				return cerr
			}
			if count >= int64(limit) {
				return fmt.Errorf("%w: this node already tracks %d files", node_file.ErrTrackedLimit, count)
			}
		}

		model := &NodeTrackedFileModel{}
		model.FromDomain(file)
		if cerr := tx.Create(model).Error; cerr != nil {
			return cerr
		}
		out = model.ToDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update saves metadata changes.
func (r *NodeTrackedFileRepository) Update(ctx context.Context, file *node_file.TrackedFile) error {
	model := &NodeTrackedFileModel{}
	model.FromDomain(file)
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).Where("id = ?", file.ID), model)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return node_file.ErrNotTracked
	}
	return nil
}

// GetByNodeIDAndPath returns one tracked file.
func (r *NodeTrackedFileRepository) GetByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.TrackedFile, error) {
	var model NodeTrackedFileModel
	err := r.db.WithContext(ctx).
		Where("node_id = ? AND path = ?", nodeID, path).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, node_file.ErrNotTracked
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

// ListByNodeID returns every tracked file for a node.
func (r *NodeTrackedFileRepository) ListByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.TrackedFile, error) {
	var models []NodeTrackedFileModel
	if err := r.db.WithContext(ctx).
		Where("node_id = ?", nodeID).
		Order("path ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	files := make([]node_file.TrackedFile, 0, len(models))
	for i := range models {
		files = append(files, *models[i].ToDomain())
	}
	return files, nil
}

// CountByNodeID counts a node's tracked files.
func (r *NodeTrackedFileRepository) CountByNodeID(ctx context.Context, nodeID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&NodeTrackedFileModel{}).
		Where("node_id = ?", nodeID).
		Count(&count).Error
	return count, err
}

// Delete removes a tracked file.
func (r *NodeTrackedFileRepository) Delete(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&NodeTrackedFileModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return node_file.ErrNotTracked
	}
	return nil
}

// DeleteByNodeID removes every tracked file of a node.
func (r *NodeTrackedFileRepository) DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&NodeTrackedFileModel{}).Error
}
