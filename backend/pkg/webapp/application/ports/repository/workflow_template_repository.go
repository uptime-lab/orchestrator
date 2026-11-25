package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/google/uuid"
)

// WorkflowTemplateRepository defines the interface for workflow template database operations
type WorkflowTemplateRepository interface {
	// Create creates a new workflow template
	Create(ctx context.Context, template *workflow_template.WorkflowTemplate) error

	// UpdateOUID writes only the template's OU scope. Re-targeting has its own
	// path because doing it through the full-document PUT means re-sending every
	// other field from the client's copy, which reverts whatever another editor
	// saved in between.
	UpdateOUIDs(ctx context.Context, accountID, templateID uuid.UUID, ouIDs []uuid.UUID, authorID *uuid.UUID) error
	// DeleteScopeByOUID drops every playbook's reference to a unit (OU-delete
	// cascade). Defensive: the delete guard refuses a unit any playbook is
	// scoped to, so this only ever clears an already-dangling reference.
	DeleteScopeByOUID(ctx context.Context, accountID, ouID uuid.UUID) error

	// Update updates an existing workflow template
	Update(ctx context.Context, template *workflow_template.WorkflowTemplate) error

	// GetByID retrieves a workflow template by ID within an account
	GetByID(ctx context.Context, accountID, templateID uuid.UUID) (*workflow_template.WorkflowTemplate, error)

	// GetAllByAccountID retrieves all workflow templates for a specific account
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]workflow_template.WorkflowTemplate, error)
	// GetAllByAccountIDWithFilters retrieves workflow templates for an account with optional filters
	GetAllByAccountIDWithFilters(ctx context.Context, accountID uuid.UUID, name *string, isShortcut *bool) ([]workflow_template.WorkflowTemplate, error)

	// Delete deletes a workflow template by ID within an account
	Delete(ctx context.Context, accountID, templateID uuid.UUID) error

	// CountByOUID reports how many templates are scoped to an organizational
	// unit. OU deletion refuses to proceed while this is non-zero, the same way
	// it refuses for member nodes — a scoped playbook whose OU vanished would
	// keep firing against an empty node set.
	CountByOUID(ctx context.Context, accountID, ouID uuid.UUID) (int64, error)

	// ListScheduledCandidates retrieves the identity and cron of every ACTIVE
	// template with a non-empty cron schedule across all accounts, ordered
	// account_id then created_at, bounded to perAccountLimit rows per account
	// (0 = unbounded).
	//
	// A projection rather than the templates themselves, and bounded in SQL
	// rather than in the caller: this runs every minute for every account on the
	// instance, while on the overwhelming majority of ticks nothing is due. The
	// graph jsonb — the expensive part of a template — is loaded only for the
	// handful that match the current minute, via GetByIDs.
	ListScheduledCandidates(ctx context.Context, perAccountLimit int) ([]workflow_template.ScheduledCandidate, error)

	// GetByIDs retrieves whole templates by id, in one query.
	//
	// Deliberately not account-scoped: the ids come from
	// ListScheduledCandidates, which spans every account by design, and each
	// returned row carries the account it belongs to. Do not reuse this for a
	// request-scoped read — GetByID is the account-checked getter.
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]workflow_template.WorkflowTemplate, error)
}
