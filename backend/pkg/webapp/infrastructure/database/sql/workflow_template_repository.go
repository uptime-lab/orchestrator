package sql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.WorkflowTemplateRepository = (*WorkflowTemplateRepository)(nil)

// WorkflowTemplateRepository is the implementation of the workflow template repository using SQL
type WorkflowTemplateRepository struct {
	db *gorm.DB
}

// autoMigrate creates the necessary database tables
func (r *WorkflowTemplateRepository) autoMigrate() error {
	return r.db.AutoMigrate(&WorkflowTemplateModel{}, &WorkflowTemplateOUModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *WorkflowTemplateRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	// One-time backfill of the legacy single ou_id column into the join table,
	// mirroring access_entry_ous. Idempotent via the composite primary key; the
	// column is left in place and never read again.
	if err := r.db.Exec(
		`INSERT INTO workflow_template_ous (template_id, ou_id, account_id)
		   SELECT id, ou_id, account_id FROM workflow_templates WHERE ou_id IS NOT NULL
		   ON CONFLICT DO NOTHING`).Error; err != nil {
		return err
	}
	return r.backfillScheduleCron()
}

// loadOUIDs fills the scope set of the given templates in one query — the list
// endpoints render a Scope column for every row, so a per-row query here is the
// N+1 that shows up as a slow Playbooks page.
func (r *WorkflowTemplateRepository) loadOUIDs(ctx context.Context, templates []workflow_template.WorkflowTemplate) error {
	if len(templates) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(templates))
	for i := range templates {
		ids = append(ids, templates[i].ID)
	}
	var rows []WorkflowTemplateOUModel
	if err := r.db.WithContext(ctx).Where("template_id IN ?", ids).
		Order("ou_id ASC").Find(&rows).Error; err != nil {
		return err
	}
	byTemplate := make(map[uuid.UUID][]uuid.UUID, len(templates))
	for _, row := range rows {
		byTemplate[row.TemplateID] = append(byTemplate[row.TemplateID], row.OUID)
	}
	for i := range templates {
		templates[i].OUIDs = byTemplate[templates[i].ID]
	}
	return nil
}

// replaceOUIDs makes the join rows match the domain set exactly. Delete-then-
// insert inside one transaction: a scope is a SET, and a merge would make
// removing a unit impossible through the same call that adds one.
func (r *WorkflowTemplateRepository) replaceOUIDs(ctx context.Context, tx *gorm.DB, accountID uuid.UUID, template *workflow_template.WorkflowTemplate) error {
	if err := tx.WithContext(ctx).Where("template_id = ?", template.ID).
		Delete(&WorkflowTemplateOUModel{}).Error; err != nil {
		return err
	}
	if len(template.OUIDs) == 0 {
		return nil
	}
	rows := make([]WorkflowTemplateOUModel, 0, len(template.OUIDs))
	seen := make(map[uuid.UUID]bool, len(template.OUIDs))
	for _, ouID := range template.OUIDs {
		if seen[ouID] {
			continue
		}
		seen[ouID] = true
		rows = append(rows, WorkflowTemplateOUModel{
			TemplateID: template.ID, OUID: ouID, AccountID: accountID,
		})
	}
	return tx.WithContext(ctx).Create(&rows).Error
}

// backfillScheduleCron fills the denormalized schedule_cron column for rows
// predating it (NULL). One-time per install: writes set the column, so no row
// stays NULL afterwards. The template table is small — loading the NULL rows
// once at startup is fine.
func (r *WorkflowTemplateRepository) backfillScheduleCron() error {
	var models []WorkflowTemplateModel
	if err := r.db.Where("schedule_cron IS NULL").Find(&models).Error; err != nil {
		return err
	}
	for i := range models {
		cron := ""
		domainTemplate, err := models[i].ToDomain()
		if err != nil {
			return err
		}
		if g := domainTemplate.EffectiveGraph(); g != nil {
			cron = g.ScheduleCron()
		}
		if err := r.db.Model(&WorkflowTemplateModel{}).
			Where("id = ?", models[i].ID).
			UpdateColumn("schedule_cron", cron).Error; err != nil {
			return err
		}
	}
	return nil
}

// NewWorkflowTemplateRepository creates a new workflow template repository
func NewWorkflowTemplateRepository(db *gorm.DB) *WorkflowTemplateRepository {
	return &WorkflowTemplateRepository{
		db: db,
	}
}

// Create creates a new workflow template
func (r *WorkflowTemplateRepository) Create(ctx context.Context, template *workflow_template.WorkflowTemplate) error {
	model := &WorkflowTemplateModel{}
	if err := model.FromDomain(template); err != nil {
		return err
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Create(model).Error; err != nil {
			return err
		}
		return r.replaceOUIDs(ctx, tx, template.AccountID, template)
	})
}

// Update updates an existing workflow template
func (r *WorkflowTemplateRepository) Update(ctx context.Context, template *workflow_template.WorkflowTemplate) error {
	model := &WorkflowTemplateModel{}
	if err := model.FromDomain(template); err != nil {
		return err
	}

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	// The row and its scope set move together: a save that wrote one and not
	// the other would leave a playbook available somewhere its document no
	// longer matches.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := updateAllColumns(tx.WithContext(ctx).
			Where("id = ? AND account_id = ?", template.ID, template.AccountID), model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNotFound
		}
		return r.replaceOUIDs(ctx, tx, template.AccountID, template)
	})
}

// GetByID retrieves a workflow template by ID within an account
func (r *WorkflowTemplateRepository) GetByID(ctx context.Context, accountID, templateID uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	var model WorkflowTemplateModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", templateID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNotFound
		}
		return nil, result.Error
	}

	domainTemplate, err := model.ToDomain()
	if err != nil {
		return nil, err
	}
	one := []workflow_template.WorkflowTemplate{*domainTemplate}
	if err := r.loadOUIDs(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

// GetAllByAccountID retrieves all workflow templates for a specific account
func (r *WorkflowTemplateRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]workflow_template.WorkflowTemplate, error) {
	var models []WorkflowTemplateModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	templates := make([]workflow_template.WorkflowTemplate, len(models))
	for i, model := range models {
		domainTemplate, err := model.ToDomain()
		if err != nil {
			return nil, err
		}
		templates[i] = *domainTemplate
	}
	if err := r.loadOUIDs(ctx, templates); err != nil {
		return nil, err
	}

	return templates, nil
}

// scheduledCandidateRow is the flat shape ListScheduledCandidates selects.
type scheduledCandidateRow struct {
	ID           uuid.UUID
	AccountID    uuid.UUID
	Name         string
	ScheduleCron string
}

// ListScheduledCandidates retrieves the identity and cron of every ACTIVE
// template with a non-empty cron schedule across all accounts (the scheduler's
// per-minute scan; the partial index on schedule_cron keeps it cheap), bounded
// to perAccountLimit rows per account.
//
// Two things are deliberate. It selects four columns and NOT the graph/steps
// jsonb, because on almost every tick nothing is due and streaming every
// tenant's playbook document to discard it is the whole cost of the scan. And
// the per-account bound is applied in SQL: doing it in the caller means the
// bound describes what gets FIRED while the query still pulls everything a
// tenant has into RAM. Ordered account_id then created_at (id breaks ties) so
// the cap picks deterministically — oldest playbooks win, tick after tick.
func (r *WorkflowTemplateRepository) ListScheduledCandidates(ctx context.Context, perAccountLimit int) ([]workflow_template.ScheduledCandidate, error) {
	const where = `status = ? AND schedule_cron IS NOT NULL AND schedule_cron <> ''`
	var rows []scheduledCandidateRow
	if perAccountLimit > 0 {
		const q = `
			SELECT id, account_id, name, schedule_cron FROM (
				SELECT id, account_id, name, schedule_cron,
					row_number() OVER (PARTITION BY account_id ORDER BY created_at ASC, id ASC) AS rn
				FROM workflow_templates
				WHERE ` + where + `
			) ranked
			WHERE rn <= ?
			ORDER BY account_id, rn`
		if err := r.db.WithContext(ctx).
			Raw(q, string(workflow_template.StatusActive), perAccountLimit).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
	} else {
		const q = `
			SELECT id, account_id, name, schedule_cron
			FROM workflow_templates
			WHERE ` + where + `
			ORDER BY account_id, created_at ASC, id ASC`
		if err := r.db.WithContext(ctx).
			Raw(q, string(workflow_template.StatusActive)).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
	}

	candidates := make([]workflow_template.ScheduledCandidate, len(rows))
	for i, row := range rows {
		candidates[i] = workflow_template.ScheduledCandidate{
			ID:        row.ID,
			AccountID: row.AccountID,
			Name:      row.Name,
			Cron:      row.ScheduleCron,
		}
	}
	return candidates, nil
}

// GetByIDs retrieves whole templates by id in one query. Unscoped by account on
// purpose — see the port's comment.
func (r *WorkflowTemplateRepository) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]workflow_template.WorkflowTemplate, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var models []WorkflowTemplateModel
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&models).Error; err != nil {
		return nil, err
	}
	templates := make([]workflow_template.WorkflowTemplate, len(models))
	for i, model := range models {
		domainTemplate, err := model.ToDomain()
		if err != nil {
			return nil, err
		}
		templates[i] = *domainTemplate
	}
	if err := r.loadOUIDs(ctx, templates); err != nil {
		return nil, err
	}
	return templates, nil
}

// GetAllByAccountIDWithFilters retrieves workflow templates for an account with optional filters
func (r *WorkflowTemplateRepository) GetAllByAccountIDWithFilters(ctx context.Context, accountID uuid.UUID, name *string, isShortcut *bool) ([]workflow_template.WorkflowTemplate, error) {
	var models []WorkflowTemplateModel
	query := r.db.WithContext(ctx).Where("account_id = ?", accountID)

	if name != nil && *name != "" {
		query = query.Where("name ILIKE ?", "%"+*name+"%")
	}

	if isShortcut != nil {
		query = query.Where("is_shortcut = ?", *isShortcut)
	}

	result := query.Order("created_at DESC").Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	templates := make([]workflow_template.WorkflowTemplate, len(models))
	for i, model := range models {
		domainTemplate, err := model.ToDomain()
		if err != nil {
			return nil, err
		}
		templates[i] = *domainTemplate
	}
	if err := r.loadOUIDs(ctx, templates); err != nil {
		return nil, err
	}

	return templates, nil
}

// CountByOUID reports how many templates are scoped to an OU (OU delete guard).
func (r *WorkflowTemplateRepository) CountByOUID(ctx context.Context, accountID, ouID uuid.UUID) (int64, error) {
	// Counts the JOIN table: the OU-delete guard must see a playbook that is
	// scoped to this unit among others, not only one scoped to it alone.
	var count int64
	err := r.db.WithContext(ctx).
		Model(&WorkflowTemplateOUModel{}).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Count(&count).Error
	return count, err
}

// Delete deletes a workflow template by ID within an account
func (r *WorkflowTemplateRepository) Delete(ctx context.Context, accountID, templateID uuid.UUID) error {
	// One transaction, and the ROW first: a delete that fails or matches
	// nothing must not have already dropped the scope rows, or a playbook that
	// still exists comes back UNSCOPED — which is the widest target set there
	// is, and on a cron playbook the next minute acts on it.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).
			Where("id = ? AND account_id = ?", templateID, accountID).
			Delete(&WorkflowTemplateModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNotFound
		}
		// The scope rows go with the playbook, or a deleted playbook keeps
		// inflating the OU-delete guard's count forever.
		return tx.WithContext(ctx).
			Where("template_id = ? AND account_id = ?", templateID, accountID).
			Delete(&WorkflowTemplateOUModel{}).Error
	})
}

// UpdateOUIDs writes the template's scope SET, the author, and nothing else.
//
// The alternative is what re-targeting used to do: re-send the whole document
// from the client's copy, which reverts every other field the client had not
// re-read. On a cron playbook containing erase_drive, the field most worth not
// reverting is this one — widening the scope from one unit to the whole account
// widens the next minute's target set.
//
// The author rides along because a re-target IS a re-authorization of where the
// playbook may run, and the scheduler re-checks the AUTHOR's privileges before
// every fire. Writing the scope without the author left a privileged cron
// playbook, whose author had since lost playbook.author_privileged, permanently
// silent after an admin re-targeted it to fix exactly that — with the response
// and the audit row both reporting the new author. A nil author (a system path
// with no user) leaves the column alone.
func (r *WorkflowTemplateRepository) UpdateOUIDs(ctx context.Context, accountID, templateID uuid.UUID, ouIDs []uuid.UUID, authorID *uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		columns := map[string]interface{}{"ou_id": firstOrNil(ouIDs), "updated_at": time.Now()}
		if authorID != nil {
			columns["author_id"] = *authorID
		}
		result := tx.WithContext(ctx).
			Model(&WorkflowTemplateModel{}).
			Where("id = ? AND account_id = ?", templateID, accountID).
			Updates(columns)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrNotFound
		}
		return r.replaceOUIDs(ctx, tx, accountID, &workflow_template.WorkflowTemplate{
			ID: templateID, OUIDs: ouIDs,
		})
	})
}

// DeleteScopeByOUID removes every playbook's reference to a unit.
//
// The delete guard refuses to remove a unit any playbook is scoped to, so in
// the normal path this finds nothing. It exists for the reference that is
// ALREADY dangling — a scope row naming a unit that vanished before the guard
// counted the join table — because validateOUs refuses every later save of a
// playbook carrying one, and nothing in the product can remove it.
func (r *WorkflowTemplateRepository) DeleteScopeByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Delete(&WorkflowTemplateOUModel{}).Error
}

// firstOrNil keeps the legacy single column populated for a rollback to an
// older binary; the join table is the truth.
func firstOrNil(ouIDs []uuid.UUID) *uuid.UUID {
	if len(ouIDs) == 0 {
		return nil
	}
	first := ouIDs[0]
	return &first
}
