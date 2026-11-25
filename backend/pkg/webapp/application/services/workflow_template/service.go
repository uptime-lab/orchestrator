package workflow_template

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// Service handles workflow template-related business logic
type Service struct {
	templateRepo    repository.WorkflowTemplateRepository
	ouRepo          repository.OrganizationalUnitRepository
	imageRepo       repository.ImageRepository  // optional; resolves image refs on import
	secretRepo      repository.SecretRepository // optional; resolves secret refs on import
	websocketClient websockets3.WebsocketClient
	audit           *audit_service.Recorder
	logger          *slog.Logger
}

// NewService creates a new workflow template service. The audit recorder is
// required: a playbook definition decides what runs unattended on the fleet,
// so its mutations are trail material (see the doctrine on audit.Recorder).
func NewService(templateRepo repository.WorkflowTemplateRepository, auditRecorder *audit_service.Recorder, logger *slog.Logger) *Service {
	if auditRecorder == nil {
		panic("workflow_template.Service requires an audit recorder")
	}
	return &Service{
		templateRepo: templateRepo,
		audit:        auditRecorder,
		logger:       logger.With("service", "workflow_template"),
	}
}

// NewServiceWithWebSocket creates a new workflow template service with websocket client
func NewServiceWithWebSocket(templateRepo repository.WorkflowTemplateRepository, ouRepo repository.OrganizationalUnitRepository, imageRepo repository.ImageRepository, secretRepo repository.SecretRepository, websocketClient websockets3.WebsocketClient, auditRecorder *audit_service.Recorder, logger *slog.Logger) *Service {
	if auditRecorder == nil {
		panic("workflow_template.Service requires an audit recorder")
	}
	return &Service{
		templateRepo:    templateRepo,
		ouRepo:          ouRepo,
		imageRepo:       imageRepo,
		secretRepo:      secretRepo,
		websocketClient: websocketClient,
		audit:           auditRecorder,
		logger:          logger.With("service", "workflow_template"),
	}
}

// auditDetail summarizes a template for the audit trail: name, status and
// scope size ONLY. Never the graph, steps or any block parameter — params can
// carry literal credentials an author typed, and audit_events is on no rewrap
// list.
func auditDetail(template *workflow_template.WorkflowTemplate) string {
	return fmt.Sprintf("name=%q status=%s ous=%d", template.Name, template.Status, len(template.OUIDs))
}

// validateOU rejects an OU id that does not exist in the account.
// validateOUs refuses a scope naming a unit that does not exist in this
// account. Every unit is checked, not just the first: a set is only as scoped
// as its least-checked member.
func (s *Service) validateOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) error {
	if s.ouRepo == nil {
		return nil
	}
	for _, ouID := range ouIDs {
		if _, err := s.ouRepo.GetByID(ctx, accountID, ouID); err != nil {
			return fmt.Errorf("%w: organizational unit not found", app_errors.ErrWorkflowTemplateInvalid)
		}
	}
	return nil
}

// Create creates a new workflow template for the authenticated account
// authorOf turns an auth-context user into an author reference, treating the
// nil UUID (system/scheduler contexts) as "no author".
func authorOf(userID uuid.UUID) *uuid.UUID {
	if userID == uuid.Nil {
		return nil
	}
	return &userID
}

func (s *Service) Create(ctx context.Context, cmd workflow_template_create.WorkflowTemplateCreateCommand) (*workflow_template.WorkflowTemplate, error) {
	// Create domain model
	template := workflow_template.NewWorkflowTemplate(
		cmd.AuthContext.AccountID,
		cmd.Name,
		cmd.Description,
		cmd.IsShortcut,
		cmd.Steps,
	)
	template.ID = cmd.ID
	template.Graph = cmd.Graph
	template.OUIDs = cmd.ScopeOUIDs()
	// Record who passed the authoring gate; the cron scheduler re-checks this
	// user's privileged-authoring right before every unattended fire.
	template.AuthorID = authorOf(cmd.AuthContext.UserID)
	if cmd.Status != "" {
		template.Status = workflow_template.WorkflowTemplateStatus(cmd.Status)
	}

	if err := s.validateOUs(ctx, cmd.AuthContext.AccountID, cmd.ScopeOUIDs()); err != nil {
		return nil, err
	}

	// Validate graph (when provided) and derive steps from it, then validate.
	// Wrapped as ErrWorkflowTemplateInvalid so the handler responds 400.
	if err := template.Normalize(); err != nil {
		return nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := template.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := refuseAddedWithdrawnBlocks(template, 0); err != nil {
		return nil, err
	}

	// Save to repository
	err := s.templateRepo.Create(ctx, template)
	if err != nil {
		return nil, err
	}

	s.audit.RecordDetail(ctx, audit_service.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionPlaybookCreate, audit_event.TargetPlaybook, template.ID.String(), auditDetail(template))

	// Emit websocket event
	s.notifyTemplateCreated(*template)

	return template, nil
}

// Update updates an existing workflow template if it belongs to the account.
//
// authorizePrivileged is re-run against the MERGED document — the graph, steps
// and OU scope that are about to be written — not against what the caller sent.
// The two differ whenever the request omits the graph, which now means
// "unchanged": the handler's own pre-check then sees an empty document and
// finds nothing privileged, while the stored privileged graph survives under a
// new `ou_id` and a new `status`. That is exactly the operation the dedicated
// re-target route refuses, and it is why SetOU already authorizes the STORED
// graph rather than the submitted one. Optional (nil skips it) for the same
// reason SetOU's is: internal callers have no request to authorize.
func (s *Service) Update(ctx context.Context, cmd workflow_template_update.WorkflowTemplateUpdateCommand,
	authorizePrivileged func(graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep, ouIDs []uuid.UUID) error) (*workflow_template.WorkflowTemplate, error) {
	// Get existing template
	existingTemplate, err := s.templateRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	// Compare-and-swap when the caller told us what they read. Second-precision
	// is deliberate: the column round-trips through Postgres and JSON, and a
	// comparison that fails on a sub-second representation difference would
	// refuse every save rather than only the racing ones.
	if cmd.ExpectedUpdatedAt != nil &&
		!existingTemplate.UpdatedAt.Truncate(time.Second).Equal(cmd.ExpectedUpdatedAt.Truncate(time.Second)) {
		return nil, fmt.Errorf("%w (stored %s, caller had %s)", app_errors.ErrWorkflowTemplateStale,
			existingTemplate.UpdatedAt.UTC().Format(time.RFC3339), cmd.ExpectedUpdatedAt.UTC().Format(time.RFC3339))
	}

	// Read before the merge: a save may keep the withdrawn blocks the stored
	// playbook already has, never add one.
	storedWithdrawn := existingTemplate.WithdrawnBlockCount()

	// Update fields
	existingTemplate.Name = cmd.Name
	existingTemplate.Description = cmd.Description
	existingTemplate.IsShortcut = cmd.IsShortcut
	// An OMITTED scope means "unchanged", for the same reason and with the same
	// blast radius as the graph below. This method already treated a missing
	// graph carefully for exactly the hand-assembling client this hurt, and the
	// scope got no such treatment: a PUT carrying neither ou_id nor ou_ids wiped
	// the join table, and a cron playbook scoped to one unit became fleet-wide on
	// the next minute's tick. An explicit `"ou_ids": []` still unscopes.
	if cmd.ScopeProvided() {
		existingTemplate.OUIDs = cmd.ScopeOUIDs()
	}
	// An OMITTED graph means "unchanged", never "delete the graph". This is a
	// full-document PUT, so a client that holds no graph — an old integration, a
	// script assembling steps by hand — would otherwise write NULL over one:
	// GraphFromSteps then rebuilds a bare trigger.start chain on read, discarding
	// every condition, join, wait, position, per-block policy AND the schedule
	// trigger, after which FromDomain recomputes schedule_cron as "" and the row
	// drops out of the scheduler's SQL prefilter. The playbook stops firing
	// permanently and nothing reports it.
	if cmd.Graph != nil {
		existingTemplate.Graph = cmd.Graph
		existingTemplate.Steps = cmd.Steps
	} else if existingTemplate.Graph == nil {
		// A genuinely legacy row (no stored graph): steps are the document.
		existingTemplate.Steps = cmd.Steps
	} else {
		// The stored graph stays the source of truth, so Normalize re-derives the
		// steps from it and any steps this caller sent are discarded. Say so.
		s.logger.WarnContext(ctx, "playbook update omitted the graph; keeping the stored one",
			"template_id", existingTemplate.ID, "account_id", cmd.AuthContext.AccountID,
			"submitted_steps", len(cmd.Steps))
	}
	// An edit re-authorizes the content, so the editor becomes the author the
	// scheduler re-checks. A no-user context (system paths) leaves it as is.
	if author := authorOf(cmd.AuthContext.UserID); author != nil {
		existingTemplate.AuthorID = author
	}
	if cmd.Status != "" {
		existingTemplate.Status = workflow_template.WorkflowTemplateStatus(cmd.Status)
	}
	if existingTemplate.Status == "" {
		existingTemplate.Status = workflow_template.StatusActive
	}
	existingTemplate.UpdatedAt = time.Now()

	// Against the MERGED scope, not the submitted one: an omitted scope keeps
	// the stored units, and validating the submission would check a set that is
	// not the one about to be stored.
	if err := s.validateOUs(ctx, cmd.AuthContext.AccountID, existingTemplate.OUIDs); err != nil {
		return nil, err
	}

	// Validate graph (when provided) and derive steps from it, then validate.
	// Wrapped as ErrWorkflowTemplateInvalid so the handler responds 400.
	if err := existingTemplate.Normalize(); err != nil {
		return nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := existingTemplate.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := refuseAddedWithdrawnBlocks(existingTemplate, storedWithdrawn); err != nil {
		return nil, err
	}

	// Authorize what is about to be STORED, after the merge and after Normalize
	// has derived the steps from the effective graph.
	if authorizePrivileged != nil {
		if err := authorizePrivileged(existingTemplate.Graph, existingTemplate.Steps, existingTemplate.OUIDs); err != nil {
			return nil, err
		}
	}

	// Save to repository
	err = s.templateRepo.Update(ctx, existingTemplate)
	if err != nil {
		return nil, err
	}

	s.audit.RecordDetail(ctx, audit_service.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionPlaybookUpdate, audit_event.TargetPlaybook, existingTemplate.ID.String(), auditDetail(existingTemplate))

	// Emit websocket event
	s.notifyTemplateUpdated(*existingTemplate)

	return existingTemplate, nil
}

// refuseAddedWithdrawnBlocks refuses a document holding more withdrawn blocks
// than the stored playbook it replaces (zero for a new one). Such a block is
// kept in the registry only so the playbooks that already carry it still open;
// every run of it is refused, so a playbook gaining one would save and activate
// cleanly and then fail on every node at that block.
func refuseAddedWithdrawnBlocks(template *workflow_template.WorkflowTemplate, stored int) error {
	if template.WithdrawnBlockCount() <= stored {
		return nil
	}
	return fmt.Errorf("%w: the \"Update boot order\" block can no longer be added to a playbook, because every run of it "+
		"is refused; change a node's boot order from its bootloader settings", app_errors.ErrWorkflowTemplateInvalid)
}

// Import creates a draft playbook from a portable document (an exported
// playbook or a built-in starter). Account-scoped references the document may
// carry are resolved against the importing account: unknown OS-image/SSH-key
// ids are cleared, missing secret names are reported — both as human-readable
// warnings rather than errors, so a shared playbook always lands as an
// editable draft.
func (s *Service) Import(ctx context.Context, cmd workflow_template_import.WorkflowTemplateImportCommand) (*workflow_template.WorkflowTemplate, []string, error) {
	if err := cmd.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}

	accountID := cmd.AuthContext.AccountID
	doc := cmd.Document
	graph := doc.Graph
	warnings := s.resolveImportReferences(ctx, accountID, graph)

	template := workflow_template.NewWorkflowTemplate(accountID, doc.Name, doc.Description, false, nil)
	template.ID = cmd.ID
	template.Graph = graph
	template.OUIDs = cmd.ScopeOUIDs()
	template.AuthorID = authorOf(cmd.AuthContext.UserID)
	// Imported content always lands as a draft: it must be reviewed (and its
	// cleared references re-picked) before run pickers or the cron scheduler
	// can see it.
	template.Status = workflow_template.StatusDraft

	if err := s.validateOUs(ctx, accountID, cmd.ScopeOUIDs()); err != nil {
		return nil, nil, err
	}
	if err := template.Normalize(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := template.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", app_errors.ErrWorkflowTemplateInvalid, err)
	}
	if err := refuseAddedWithdrawnBlocks(template, 0); err != nil {
		return nil, nil, err
	}

	if err := s.templateRepo.Create(ctx, template); err != nil {
		return nil, nil, err
	}

	// The warnings COUNT is trail-safe; the warning texts name cleared params
	// and secret names, which are identifiers, but the count answers the
	// auditor's question ("did it land clean?") without copying free text.
	s.audit.RecordDetail(ctx, audit_service.UserActor(accountID, cmd.AuthContext.UserID),
		audit_event.ActionPlaybookImport, audit_event.TargetPlaybook, template.ID.String(),
		fmt.Sprintf("%s warnings=%d", auditDetail(template), len(warnings)))

	s.notifyTemplateCreated(*template)

	return template, warnings, nil
}

// resolveImportReferences validates the account-scoped references of an
// incoming graph and mutates it in place: id parameters that do not resolve
// in this account are removed. Returns warnings describing what was changed
// or needs attention.
func (s *Service) resolveImportReferences(ctx context.Context, accountID uuid.UUID, graph *workflow_template.WorkflowGraph) []string {
	var warnings []string

	for _, ref := range workflow_template.AccountScopedParams(graph) {
		if s.refResolves(ctx, accountID, ref) {
			continue
		}
		delete(graph.Nodes[ref.NodeIndex].Params, ref.Param)
		warnings = append(warnings, fmt.Sprintf(
			"step '%s': parameter '%s' referenced an id that does not exist in this account and was cleared — pick a new value",
			ref.NodeLabel, ref.Param))
	}

	if s.secretRepo != nil {
		for _, name := range workflow_template.SecretReferences(graph) {
			if _, err := s.secretRepo.GetByName(ctx, accountID, name); err != nil {
				if errors.Is(err, app_errors.ErrSecretNotFound) || errors.Is(err, app_errors.ErrNotFound) {
					warnings = append(warnings, fmt.Sprintf(
						"references secret '%s' which does not exist in this account — create it before running", name))
				}
				// Infrastructure errors are not the importer's problem; the
				// reference stays as-is and resolves (or not) at run time.
			}
		}
	}

	return warnings
}

// refResolves reports whether an account-scoped id parameter points at an
// existing entity of the importing account. Unknown types and absent
// repositories fail closed: the reference is treated as unresolved.
func (s *Service) refResolves(ctx context.Context, accountID uuid.UUID, ref workflow_template.AccountScopedParam) bool {
	id, err := uuid.Parse(ref.Value)
	if err != nil {
		return false
	}
	switch ref.Type {
	case workflow_block.ParamOSImage:
		if s.imageRepo == nil {
			return false
		}
		_, err := s.imageRepo.GetByID(ctx, accountID, id)
		return err == nil
	default:
		return false
	}
}

// GetByID retrieves a workflow template by ID if it belongs to the account
func (s *Service) GetByID(ctx context.Context, query workflow_template_get.WorkflowTemplateGetQuery) (*workflow_template.WorkflowTemplate, error) {
	template, err := s.templateRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
	if err != nil {
		return nil, err
	}

	return template, nil
}

// GetAllByAccountID lists all workflow templates for the account
func (s *Service) GetAllByAccountID(ctx context.Context, query workflow_template_list.WorkflowTemplateListQuery) ([]workflow_template.WorkflowTemplate, error) {
	// Use filtered method if any filters are provided
	if query.Name != nil || query.IsShortcut != nil {
		templates, err := s.templateRepo.GetAllByAccountIDWithFilters(ctx, query.AuthContext.AccountID, query.Name, query.IsShortcut)
		if err != nil {
			return nil, err
		}
		return templates, nil
	}

	// Otherwise use the simple method
	templates, err := s.templateRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}

	return templates, nil
}

// Delete deletes a workflow template if it belongs to the account. The userID
// is the actor for the audit trail (uuid.Nil for system callers).
func (s *Service) Delete(ctx context.Context, accountID, userID, templateID uuid.UUID) error {
	// Get template before deletion for websocket event
	templateToDelete, err := s.templateRepo.GetByID(ctx, accountID, templateID)
	if err != nil {
		return err
	}

	err = s.templateRepo.Delete(ctx, accountID, templateID)
	if err != nil {
		return err
	}

	s.audit.RecordDetail(ctx, audit_service.UserActor(accountID, userID),
		audit_event.ActionPlaybookDelete, audit_event.TargetPlaybook, templateID.String(), auditDetail(templateToDelete))

	// Emit websocket event
	s.notifyTemplateDeleted(*templateToDelete)

	return nil
}

// notifyTemplateCreated sends a template created event via websocket
func (s *Service) notifyTemplateCreated(template workflow_template.WorkflowTemplate) {
	if s.websocketClient == nil {
		return
	}
	// WebSocket notification can be added later if needed
	s.logger.Debug("workflow template created", "template_id", template.ID)
}

// notifyTemplateUpdated sends a template updated event via websocket
func (s *Service) notifyTemplateUpdated(template workflow_template.WorkflowTemplate) {
	if s.websocketClient == nil {
		return
	}
	// WebSocket notification can be added later if needed
	s.logger.Debug("workflow template updated", "template_id", template.ID)
}

// notifyTemplateDeleted sends a template deleted event via websocket
func (s *Service) notifyTemplateDeleted(template workflow_template.WorkflowTemplate) {
	if s.websocketClient == nil {
		return
	}
	// WebSocket notification can be added later if needed
	s.logger.Debug("workflow template deleted", "template_id", template.ID)
}

// SetOU re-targets a playbook's organizational-unit scope and nothing else.
//
// Its own operation, following the deliberate precedent set by access entries:
// a scope change carried inside a full-document PUT means the client re-sends
// every field it happens to hold, so an operator dragging a playbook onto a unit
// silently reverts whatever another operator saved between their page load and
// the drag.
//
// The privileged-authoring check is re-run against the NEW scope with the
// STORED graph, because the answer depends on both, and the stored graph is the
// one that will actually run.
func (s *Service) SetOUs(ctx context.Context, accountID, userID, templateID uuid.UUID, ouIDs []uuid.UUID,
	authorizePrivileged func(graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep, ouIDs []uuid.UUID) error) (*workflow_template.WorkflowTemplate, error) {
	existing, err := s.templateRepo.GetByID(ctx, accountID, templateID)
	if err != nil {
		return nil, err
	}
	if err := s.validateOUs(ctx, accountID, ouIDs); err != nil {
		return nil, err
	}
	if authorizePrivileged != nil {
		if err := authorizePrivileged(existing.Graph, existing.Steps, ouIDs); err != nil {
			return nil, err
		}
	}
	// An OU change re-authorizes WHERE the playbook may run, so the operator who
	// made it becomes the author the scheduler re-checks — the same rule Update
	// follows. It has to be WRITTEN, not just set on the copy that is returned:
	// the author is what the scheduler re-checks before every fire, so a
	// privileged cron playbook whose original author has since lost
	// playbook.author_privileged stayed permanently silent after an admin
	// re-targeted it, while the response and the audit row said it had a new one.
	author := authorOf(userID)
	if err := s.templateRepo.UpdateOUIDs(ctx, accountID, templateID, ouIDs, author); err != nil {
		return nil, err
	}

	existing.OUIDs = ouIDs
	existing.UpdatedAt = time.Now()
	if author != nil {
		existing.AuthorID = author
	}

	s.audit.RecordDetail(ctx, audit_service.UserActor(accountID, userID),
		audit_event.ActionPlaybookRetargetOU, audit_event.TargetPlaybook, templateID.String(), auditDetail(existing))

	s.notifyTemplateUpdated(*existing)
	return existing, nil
}
