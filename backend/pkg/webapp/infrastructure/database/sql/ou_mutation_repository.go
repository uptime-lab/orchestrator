package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.OUMutationRepository = (*OUMutationRepository)(nil)

// OUMutationRepository implements the serialization-guarded OU mutations.
type OUMutationRepository struct {
	db *gorm.DB
}

// NewOUMutationRepository wires the guarded OU mutation repository.
func NewOUMutationRepository(db *gorm.DB) *OUMutationRepository {
	if db == nil {
		panic("OUMutationRepository requires a database handle")
	}
	return &OUMutationRepository{db: db}
}

// lockOURow takes a row lock on the unit and reports whether it exists. The
// strength is the caller's: clause.Locking{Strength: "UPDATE"} for the delete,
// "SHARE" for an assignment. Two SHARE holders proceed together (concurrent
// assignments into the same unit do not need to serialize against each other);
// UPDATE excludes both.
func lockOURow(tx *gorm.DB, accountID, ouID uuid.UUID, strength string) error {
	var found OrganizationalUnitModel
	err := tx.Clauses(clause.Locking{Strength: strength}).
		Select("id").
		Where("id = ? AND account_id = ?", ouID, accountID).
		First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app_errors.ErrOrganizationalUnitNotFound
	}
	return err
}

// DeleteIfUnreferenced re-checks every emptiness guard inside the transaction
// that deletes, with the unit's row locked for the whole of it.
func (r *OUMutationRepository) DeleteIfUnreferenced(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOURow(tx, accountID, ouID, "UPDATE"); err != nil {
			return err
		}

		guards := []struct {
			model interface{}
			where string
			err   error
		}{
			{&OrganizationalUnitModel{}, "parent_id", app_errors.ErrOrganizationalUnitHasChild},
			{&NodeModel{}, "ou_id", app_errors.ErrOrganizationalUnitHasNodes},
			// The JOIN table, not workflow_templates.ou_id. That column now
			// carries one representative unit of a playbook's scope SET, so
			// guarding on it would let the 2nd..Nth unit be deleted out from
			// under a playbook — leaving a scope row naming a unit that no
			// longer exists, which validateOUs then refuses on every later
			// save, with no gesture in the product able to remove it.
			{&WorkflowTemplateOUModel{}, "ou_id", app_errors.ErrOrganizationalUnitHasPlaybooks},
			// The JOIN table, not utility_assignments.ou_id — that column is
			// legacy (backfilled once, never read), and guarding on it would
			// let the 2nd..Nth unit of a scope be deleted out from under an
			// assignment: the exact ghost-unit defect workflow_template_ous
			// documents two entries up.
			{&UtilityAssignmentOUModel{}, "ou_id", app_errors.ErrOrganizationalUnitHasUtilities},
		}
		for _, guard := range guards {
			var count int64
			if err := tx.Model(guard.model).
				Where("account_id = ? AND "+guard.where+" = ?", accountID, ouID).
				Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return guard.err
			}
		}

		result := tx.Where("id = ? AND account_id = ?", ouID, accountID).
			Delete(&OrganizationalUnitModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrOrganizationalUnitNotFound
		}
		return nil
	})
}

// AssignNodeOU writes the node's ou_id under a share lock on the target unit.
func (r *OUMutationRepository) AssignNodeOU(ctx context.Context, accountID, nodeID uuid.UUID, ouID *uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if ouID != nil {
			if err := lockOURow(tx, accountID, *ouID, "SHARE"); err != nil {
				return err
			}
		}

		// One column, not a full-row Save: a node row carries machine-owned
		// state (the `restarting` pin, connection status, hardware facts) that
		// the gRPC layer writes continuously, and rewriting it from a row read
		// moments ago silently reverts whatever landed in between.
		result := tx.Model(&NodeModel{}).
			Where("id = ? AND account_id = ?", nodeID, accountID).
			Updates(map[string]interface{}{"ou_id": ouID, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNodeNotFound
		}
		return nil
	})
}
