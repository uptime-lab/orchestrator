package sql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

var _ repository.UtilityAssignmentRepository = (*UtilityAssignmentRepository)(nil)

// UtilityAssignmentRepository persists utility assignments and their OU set
// (utility_assignment_ous join table). Reads hydrate each assignment's OUIDs.
type UtilityAssignmentRepository struct {
	db *gorm.DB
}

func NewUtilityAssignmentRepository(db *gorm.DB) *UtilityAssignmentRepository {
	return &UtilityAssignmentRepository{db: db}
}

func (r *UtilityAssignmentRepository) Setup() error {
	if err := r.db.AutoMigrate(&UtilityAssignmentModel{}, &UtilityAssignmentOUModel{}); err != nil {
		return err
	}
	for _, stmt := range []string{
		// One-time backfill: move the legacy single ou_id into the join table
		// (an assignment can now span several units). Idempotent via the
		// composite PK; the column stays but is never read again.
		`INSERT INTO utility_assignment_ous (assignment_id, ou_id, account_id)
		   SELECT id, ou_id, account_id FROM utility_assignments WHERE ou_id IS NOT NULL
		   ON CONFLICT DO NOTHING`,
		// The old per-OU unique index keyed on the ou_id column; with the join
		// table it would misfire (every new assignment has ou_id NULL).
		// OU-level one-utility-per-unit uniqueness cannot be an index over a
		// SET scope; the service runs the friendly early check, and Create/
		// Update re-run it inside the write transaction under the account's
		// advisory lock (refuseOUOverlapLocked) — the authority under
		// concurrency. Node and account-wide levels keep DB indexes.
		`DROP INDEX IF EXISTS uq_utility_assignment_ou`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_utility_assignment_node ON utility_assignments (account_id, utility_id, node_id) WHERE node_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_utility_assignment_account ON utility_assignments (account_id, utility_id) WHERE account_wide`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// UtilityAssignmentLockClass is the advisory-lock class ("ORUA") for
// per-account assignment writes that carry an OU set. Node-level and
// account-wide uniqueness are DB partial indexes; the OU-set tie-freedom rule
// cannot be an index, so the write transaction takes this lock and re-runs the
// overlap check itself (the image-caps shape: the service's check stays as the
// early friendly refusal, the repository is the authority under concurrency).
const UtilityAssignmentLockClass = 0x4F525541

// refuseOUOverlapLocked re-runs the tie-freedom rule INSIDE the write
// transaction: no other assignment of the same utility may cover any of the
// target units. Callers hold the account's advisory lock, so two concurrent
// writes cannot both pass.
func refuseOUOverlapLocked(tx *gorm.DB, accountID uuid.UUID, utilityID string, assignmentID uuid.UUID, ouIDs []uuid.UUID) error {
	if len(ouIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&UtilityAssignmentOUModel{}).
		Joins("JOIN utility_assignments a ON a.id = utility_assignment_ous.assignment_id").
		Where("utility_assignment_ous.account_id = ? AND a.utility_id = ? AND utility_assignment_ous.assignment_id <> ? AND utility_assignment_ous.ou_id IN ?",
			accountID, utilityID, assignmentID, ouIDs).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return utility.ErrDuplicateAssignment
	}
	return nil
}

func (r *UtilityAssignmentRepository) Create(ctx context.Context, a *utility.Assignment) error {
	model := &UtilityAssignmentModel{}
	model.fromDomain(a)
	// The columns' now() defaults are never read back into the entity, so a
	// caller that set no times would answer with zero ones: write explicit
	// times and hand them back.
	if model.CreatedAt.IsZero() {
		model.CreatedAt = time.Now()
	}
	if model.UpdatedAt.IsZero() {
		model.UpdatedAt = model.CreatedAt
	}
	a.CreatedAt, a.UpdatedAt = model.CreatedAt, model.UpdatedAt
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(a.OUIDs) > 0 {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
				UtilityAssignmentLockClass, a.AccountID.String()).Error; err != nil {
				return err
			}
			if err := refuseOUOverlapLocked(tx, a.AccountID, a.UtilityID, a.ID, a.OUIDs); err != nil {
				return err
			}
		}
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return utility.ErrDuplicateAssignment
			}
			return err
		}
		return r.insertOUs(tx, a.AccountID, a.ID, a.OUIDs)
	})
}

// utilityAssignmentUpdateColumns are the columns Update writes. The
// repository test holds every model column to this list or to its named
// exceptions.
var utilityAssignmentUpdateColumns = []string{"config", "enabled", "account_wide", "node_id", "updated_at"}

func (r *UtilityAssignmentRepository) Update(ctx context.Context, a *utility.Assignment) error {
	model := &UtilityAssignmentModel{}
	model.fromDomain(a)
	if model.UpdatedAt.IsZero() {
		model.UpdatedAt = time.Now()
	}
	// The caller's entity reports the time the row is stored with.
	a.UpdatedAt = model.UpdatedAt
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(a.OUIDs) > 0 {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
				UtilityAssignmentLockClass, a.AccountID.String()).Error; err != nil {
				return err
			}
			if err := refuseOUOverlapLocked(tx, a.AccountID, a.UtilityID, a.ID, a.OUIDs); err != nil {
				return err
			}
		}
		result := tx.
			Model(&UtilityAssignmentModel{}).
			Where("id = ? AND account_id = ?", a.ID, a.AccountID).
			// Scope columns are written too so a re-target persists; a plain
			// config edit carries the loaded values and is a scope no-op.
			Select(utilityAssignmentUpdateColumns).
			Updates(model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
				return utility.ErrDuplicateAssignment
			}
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		// Replace the OU set wholesale: the domain entity carries the full
		// desired set (the access-entry update pattern).
		if err := tx.Where("assignment_id = ? AND account_id = ?", a.ID, a.AccountID).
			Delete(&UtilityAssignmentOUModel{}).Error; err != nil {
			return err
		}
		return r.insertOUs(tx, a.AccountID, a.ID, a.OUIDs)
	})
}

func (r *UtilityAssignmentRepository) insertOUs(tx *gorm.DB, accountID, assignmentID uuid.UUID, ouIDs []uuid.UUID) error {
	for _, ouID := range ouIDs {
		// SHARE-lock the unit's row first (the AssignNodeOU pattern): an OU
		// delete holds FOR UPDATE while it counts this join table, so without
		// this a retarget could commit a join row between the delete's count
		// and its DELETE — a ghost unit that validateOUs then refuses on every
		// later save. With the lock, either the delete finishes first and this
		// insert refuses ("organizational unit not found"), or this insert
		// commits first and the delete's count sees the row and refuses.
		if err := lockOURow(tx, accountID, ouID, "SHARE"); err != nil {
			return err
		}
		row := &UtilityAssignmentOUModel{AssignmentID: assignmentID, OUID: ouID, AccountID: accountID}
		if err := tx.Where(row).FirstOrCreate(row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *UtilityAssignmentRepository) GetByID(ctx context.Context, accountID, id uuid.UUID) (*utility.Assignment, error) {
	var model UtilityAssignmentModel
	if err := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&model).Error; err != nil {
		return nil, err
	}
	a := model.toDomain()
	if err := r.hydrateOUs(ctx, accountID, []*utility.Assignment{a}); err != nil {
		return nil, err
	}
	return a, nil
}

func (r *UtilityAssignmentRepository) Delete(ctx context.Context, accountID, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("assignment_id = ? AND account_id = ?", id, accountID).
			Delete(&UtilityAssignmentOUModel{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ? AND account_id = ?", id, accountID).
			Delete(&UtilityAssignmentModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *UtilityAssignmentRepository) ListByAccount(ctx context.Context, accountID uuid.UUID) ([]utility.Assignment, error) {
	var models []UtilityAssignmentModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]utility.Assignment, 0, len(models))
	refs := make([]*utility.Assignment, 0, len(models))
	for i := range models {
		out = append(out, *models[i].toDomain())
		refs = append(refs, &out[len(out)-1])
	}
	if err := r.hydrateOUs(ctx, accountID, refs); err != nil {
		return nil, err
	}
	return out, nil
}

// hydrateOUs fills OUIDs for a batch in ONE query (the list page reads every
// assignment; N+1 across a 500-row cap is a self-inflicted slow page).
func (r *UtilityAssignmentRepository) hydrateOUs(ctx context.Context, accountID uuid.UUID, list []*utility.Assignment) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(list))
	byID := make(map[uuid.UUID]*utility.Assignment, len(list))
	for _, a := range list {
		ids = append(ids, a.ID)
		byID[a.ID] = a
	}
	var rows []UtilityAssignmentOUModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND assignment_id IN ?", accountID, ids).
		Order("ou_id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if a, ok := byID[row.AssignmentID]; ok {
			a.OUIDs = append(a.OUIDs, row.OUID)
		}
	}
	return nil
}

func (r *UtilityAssignmentRepository) CountByAccount(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&UtilityAssignmentModel{}).
		Where("account_id = ?", accountID).
		Count(&count).Error
	return count, err
}

// OUCounts counts ASSIGNMENTS per unit via the join table — the rail facet.
func (r *UtilityAssignmentRepository) OUCounts(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int64, error) {
	// The column tag is load-bearing: GORM's naming maps the field OUID to
	// "ouid", not "ou_id", so an untagged scan lands every group in the zero
	// UUID — the rail showed one ghost bucket while every real unit read 0.
	var rows []struct {
		OUID  uuid.UUID `gorm:"column:ou_id"`
		Count int64     `gorm:"column:count"`
	}
	if err := r.db.WithContext(ctx).
		Model(&UtilityAssignmentOUModel{}).
		Select("ou_id, COUNT(*) as count").
		Where("account_id = ?", accountID).
		Group("ou_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.OUID] = row.Count
	}
	return out, nil
}
