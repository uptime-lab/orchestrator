package workflow

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_execution_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_execution_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_template_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	workflow_template_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/cronexpr"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// WorkflowHandler handles workflow-related HTTP requests
type WorkflowHandler struct {
	templateService  *workflow_template_service.Service
	executionService *workflow_execution_service.Service
	enforcer         *authz_service.Enforcer
	audit            *audit.Recorder
	logger           *slog.Logger
}

// NewWorkflowHandler creates a new workflow handler
func NewWorkflowHandler(
	templateService *workflow_template_service.Service,
	executionService *workflow_execution_service.Service,
	enforcer *authz_service.Enforcer,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *WorkflowHandler {
	// Required: the run and command endpoints here serve resolved secret
	// material to a caller holding secret.read_value, and that read has to be
	// recorded.
	if auditRecorder == nil {
		panic("WorkflowHandler requires an audit recorder")
	}
	return &WorkflowHandler{
		templateService:  templateService,
		executionService: executionService,
		enforcer:         enforcer,
		audit:            auditRecorder,
		logger:           logger.With("handler", "workflow"),
	}
}

// requiredRunAction returns the action needed to run/author a template: the
// privileged variant if any block can run scripts, install OS, write files,
// flash EEPROM, upsert secrets or make HTTP calls; the basic variant
// otherwise.
func requiredRunAction(blockTypes []string, basic, privileged authz.Action) authz.Action {
	for _, t := range blockTypes {
		if authz.IsPrivilegedBlock(t) {
			return privileged
		}
	}
	return basic
}

// blockTypesFor extracts the distinct block types of the graph the caller is
// submitting (graph is source of truth; steps synthesize a graph otherwise),
// so authoring can be gated by content BEFORE the template is stored.
func blockTypesFor(graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep) []string {
	tmpl := &workflow_template.WorkflowTemplate{Graph: graph, Steps: steps}
	return tmpl.BlockTypes()
}

// authorizePrivilegedAuthoring rejects saving a template that contains any
// privileged block unless the caller holds playbook.author_privileged (admin+)
// in the template's OU scope. This is what stops a manager from smuggling
// run_script/install_os/secret_upsert into a template that the cron scheduler
// would then run with no per-run authz. Non-privileged authoring is covered by
// the route-level playbook.author middleware. Returns a non-nil error to
// propagate, nil when allowed.
// A SET of units is authorized unit by unit, and every one must allow it. A
// playbook attached to two units runs in both, so the right to author it there
// has to be held in both — checking a representative member would let someone
// who may author in unit A attach privileged content to unit B.
func (h *WorkflowHandler) authorizePrivilegedAuthoring(c echo.Context, graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep, ouIDs []uuid.UUID) error {
	action := requiredRunAction(blockTypesFor(graph, steps), authz.ActionPlaybookAuthor, authz.ActionPlaybookAuthorPrivileged)
	// Feeding a secret into a block that is NOT privileged is itself a
	// privileged act, whatever the block's type says. The live case is
	// control.condition: comparing {{secrets.api_key}} to a literal answers
	// "did it match?" through the port it routes to, so the run's own block
	// statuses spell the value out one comparison at a time — with `contains`,
	// character by character — while the probe graph touches no device and
	// contains no privileged block for the type check to catch.
	//
	// This is the same reasoning that already made command.set_hostname
	// privileged INSIDE a playbook, generalised from one block to the rule: the
	// engine resolves a value the author may not read into a field the author
	// can read back. Every block that legitimately consumes secrets is already
	// privileged, so this refuses the side channel and nothing else.
	if action != authz.ActionPlaybookAuthorPrivileged {
		// Read off the EFFECTIVE graph, so a steps-only submission is checked
		// against the graph it will be stored and run as.
		effective := (&workflow_template.WorkflowTemplate{Graph: graph, Steps: steps}).EffectiveGraph()
		if secretFed := effective.SecretFedUnprivilegedBlocks(); len(secretFed) > 0 {
			h.logger.InfoContext(c.Request().Context(),
				"playbook feeds secret material into an unprivileged block; requiring privileged authoring",
				"blocks", secretFed)
			action = authz.ActionPlaybookAuthorPrivileged
		}
	}
	if action != authz.ActionPlaybookAuthorPrivileged {
		return nil // no privileged blocks — route-level author gate suffices
	}
	if len(ouIDs) == 0 {
		return middleware.Authorize(c, h.enforcer, authz.ActionPlaybookAuthorPrivileged, nil)
	}
	for i := range ouIDs {
		ouID := ouIDs[i]
		if err := middleware.Authorize(c, h.enforcer, authz.ActionPlaybookAuthorPrivileged, &ouID); err != nil {
			return err
		}
	}
	return nil
}

// RegisterRoutes registers workflow routes
func (h *WorkflowHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	// Workflow template routes
	templates := e.Group("/api/v1/workflow-templates")
	templates.Use(authMiddleware)

	// Authoring is manager+; running a privileged playbook is separately gated
	// at StartExecution (admin+), which is the real boundary — a manager may
	// draft a privileged template but cannot run it.
	author := middleware.RequireAction(h.enforcer, authz.ActionPlaybookAuthor)
	readTemplate := middleware.RequireAction(h.enforcer, authz.ActionPlaybookRead)
	templates.POST("", h.CreateTemplate, author)
	templates.GET("", h.ListTemplates, readTemplate)
	// Static segments take precedence over :id in Echo's router, so these
	// never shadow template ids.
	templates.GET("/starters", h.ListStarters, readTemplate)
	templates.POST("/import", h.ImportTemplate, author)
	templates.GET("/:id", h.GetTemplate, readTemplate)
	templates.GET("/:id/export", h.ExportTemplate, readTemplate)
	templates.PUT("/:id", h.UpdateTemplate, author)
	// Re-targeting is its own route, deliberately, the way access entries
	// already do it: doing it through the full-document PUT means the client
	// re-sends every field it holds, so a drag onto a unit reverts whatever
	// another operator saved since the page loaded.
	templates.PUT("/:id/organizational-unit", h.SetTemplateOU, author)
	templates.DELETE("/:id", h.DeleteTemplate, author)

	// Workflow execution routes
	executions := e.Group("/api/v1/workflow-executions")
	executions.Use(authMiddleware)

	// Reads carry run history — block outputs, error text, command payloads —
	// so they need an action check of their own. Authentication alone was the
	// only gate here; StartExecution was the sole authorized route.
	readPlaybook := middleware.RequireAction(h.enforcer, authz.ActionPlaybookRead)
	executions.POST("", h.StartExecution)
	executions.GET("", h.ListExecutions, readPlaybook)
	executions.GET("/:id", h.GetExecution, readPlaybook)
	executions.GET("/:id/nodes", h.GetExecutionNodes, readPlaybook)
	executions.GET("/:id/nodes/:nodeId/commands", h.GetExecutionNodeCommands, readPlaybook)
	// Cancelling is gated on playbook.run, the counterpart of starting — NOT on
	// run_privileged. A cancel only ever STOPS work; requiring the privileged
	// action would mean the operator who can least afford a stuck run (one who
	// may not start privileged playbooks) also cannot clear one somebody else
	// left behind, and a run pinned active costs the whole account its cap.
	executions.POST("/:id/cancel", h.CancelExecution, middleware.RequireAction(h.enforcer, authz.ActionPlaybookRun))

	// Workflow block registry routes
	blocks := e.Group("/api/v1/workflow-blocks")
	blocks.Use(authMiddleware)

	blocks.GET("", h.ListBlocks)

	// Schedule preview: a pure calculator over a cron string, carrying no
	// account data at all — the same shape as the block registry above, which
	// is why it needs authentication but no action. It exists so the editor
	// never has to reimplement the dialect in TypeScript: what fires, what a
	// schedule MEANS and whether it fires at all are then one answer.
	schedule := e.Group("/api/v1/schedule")
	schedule.Use(authMiddleware)
	schedule.POST("/preview", h.PreviewSchedule)
}

// SchedulePreviewRequest is the body of the schedule preview route.
type SchedulePreviewRequest struct {
	Cron string `json:"cron"`
	// Count caps how many upcoming runs to return (default 5, max 10).
	Count int `json:"count"`
}

// PreviewSchedule explains a cron expression and lists its upcoming runs
// @Summary Preview a cron schedule
// @Description Validates a 5-field cron expression and returns a human description plus the next UTC run times. A valid expression that never fires (for example '0 0 30 2 *') returns an empty run list rather than an error.
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param request body SchedulePreviewRequest true "Cron expression to preview"
// @Success 200 {object} results.APIResponse{data=results.SchedulePreviewResult} "Schedule preview"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/schedule/preview [post]
func (h *WorkflowHandler) PreviewSchedule(c echo.Context) error {
	var req SchedulePreviewRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	count := req.Count
	if count <= 0 {
		count = 5
	}
	if count > 10 {
		count = 10
	}

	// An INVALID expression is answered 200 with valid=false, not 400: this
	// route is called while the operator is still typing, and a red HTTP error
	// per keystroke is noise. The save path is where a bad cron is refused.
	schedule, err := cronexpr.Compile(strings.TrimSpace(req.Cron))
	if err != nil {
		return results.SendOKResponse(c, results.SchedulePreviewResult{
			Valid: false,
			Error: err.Error(),
		}, "schedule preview")
	}

	runs := schedule.NextRuns(time.Now().UTC(), count)
	next := make([]time.Time, 0, len(runs))
	next = append(next, runs...)

	return results.SendOKResponse(c, results.SchedulePreviewResult{
		Valid:       true,
		Description: schedule.Describe(),
		NextRuns:    next,
		// Distinguishes "no upcoming runs because the horizon is short" from
		// "this expression can never fire", which is a real and invisible
		// mistake — February has no 30th.
		NeverFires: len(runs) == 0,
	}, "schedule preview")
}

// ListBlocks returns the workflow block registry
// @Summary List workflow blocks
// @Description Returns the catalog of block types available for workflow templates, including parameter schemas used to render editor forms and validate payloads
// @Tags workflow-blocks
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.WorkflowBlockResult} "Block definitions"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/workflow-blocks [get]
func (h *WorkflowHandler) ListBlocks(c echo.Context) error {
	blocks := results.NewWorkflowBlockResults(workflow_block.Registry())
	return results.SendOKResponse(c, blocks, "workflow blocks retrieved successfully")
}

// CreateTemplate creates a new workflow template
// @Summary Create workflow template
// @Description Creates a new workflow template
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param template body workflow_template_create.WorkflowTemplateCreateCommand true "Workflow template creation data"
// @Success 201 {object} results.APIResponse{data=results.WorkflowTemplateResult} "Template created successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates [post]
func (h *WorkflowHandler) CreateTemplate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Nothing here may log or echo step params. They are the same fields
	// results/command_redaction.go treats as secret-bearing and
	// node_command_model.go encrypts at rest — a
	// provision_connection password, an "Authorization: Bearer …" header on
	// download_file, an inline run_script. This handler used to read the raw
	// body and write it verbatim at Info on every create, and to interpolate
	// %+v of a whole step into both the log and the 400 response, which put
	// that material in the container log in the clear.
	var cmd workflow_template_create.WorkflowTemplateCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request",
			"error", err,
			"account_id", accountID,
		)
		return results.SendValidationError(c, bindErrorMessage(err))
	}

	h.logger.Info("received workflow template create command",
		"name", cmd.Name,
		"steps_count", len(cmd.Steps),
		"account_id", accountID,
	)

	// Validate steps structure before processing
	for i, step := range cmd.Steps {
		// Check if command is zero value (uninitialized)
		if step.Command.Slug == "" {
			h.logger.Error("step missing command slug",
				"step_index", i,
				"step_order", step.Order,
				"step_name", step.Name,
				"account_id", accountID,
			)
			return results.SendValidationError(c, fmt.Sprintf(
				"step %d (order=%d, name=%q): command.slug is required", i, step.Order, step.Name))
		}
		h.logger.Debug("validating step",
			"step_index", i,
			"step_order", step.Order,
			"command_slug", step.Command.Slug,
			"payload_keys", len(step.Command.Payload),
		)
	}

	// Set auth context and generate ID
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Authoring a template with privileged blocks requires admin+; otherwise a
	// manager could smuggle run_script/install_os into a scheduled template the
	// cron engine runs without per-run authz.
	if err := h.authorizePrivilegedAuthoring(c, cmd.Graph, cmd.Steps, cmd.ScopeOUIDs()); err != nil {
		return err
	}

	template, err := h.templateService.Create(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to create template", err,
			"account_id", accountID,
			"name", cmd.Name,
			"steps_count", len(cmd.Steps),
		)
		if errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, fmt.Sprintf("failed to create template: %v", err), err)
	}

	templateResult := results.NewWorkflowTemplateResult(template)
	return results.SendCreatedResponse(c, templateResult, "template created successfully")
}

// GetTemplate retrieves a workflow template by ID
// @Summary Get workflow template by ID
// @Description Retrieves workflow template details by ID
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Template ID"
// @Success 200 {object} results.APIResponse{data=results.WorkflowTemplateResult} "Template retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Template not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates/{id} [get]
func (h *WorkflowHandler) GetTemplate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse template ID from URL parameter
	templateIDStr := c.Param("id")
	templateID, err := uuid.Parse(templateIDStr)
	if err != nil {
		h.logger.Warn("invalid template ID", "error", err, "template_id", templateIDStr)
		return results.SendValidationError(c, "invalid template ID")
	}

	query := workflow_template_get.WorkflowTemplateGetQuery{
		ID:          templateID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	template, err := h.templateService.GetByID(c.Request().Context(), query)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get template", err, "template_id", templateID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "template not found")
		}
		return results.SendInternalServerError(c, "failed to get template", err)
	}

	templateResult := results.NewWorkflowTemplateResult(template)
	// Authoring redaction: literal credentials typed into free-text params are
	// readable at playbook.read, which RoleViewer holds. The save path refuses
	// a document that carries the placeholder back, so a redacted read cannot
	// silently blank a script.
	if !h.callerCanReadSecretValues(c, accountID) {
		templateResult.RedactAuthoring()
	}
	return results.SendOKResponse(c, templateResult, "template retrieved successfully")
}

// ListTemplates retrieves all workflow templates for the account
// @Summary List workflow templates
// @Description Retrieves a list of all workflow templates for the authenticated account. Optional filters: name, is_shortcut, ou_id (playbooks whose OU scope set contains the unit).
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param name query string false "Filter by name"
// @Param is_shortcut query boolean false "Filter shortcuts"
// @Param ou_id query string false "Filter by OU scope membership (UUID)"
// @Success 200 {object} results.APIResponse{data=[]results.WorkflowTemplateResult} "Templates retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates [get]
func (h *WorkflowHandler) ListTemplates(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse query parameters for filtering
	var nameFilter *string
	if nameParam := c.QueryParam("name"); nameParam != "" {
		nameFilter = &nameParam
	}

	var isShortcutFilter *bool
	if isShortcutParam := c.QueryParam("is_shortcut"); isShortcutParam != "" {
		isShortcut := isShortcutParam == "true" || isShortcutParam == "1"
		isShortcutFilter = &isShortcut
	}

	// Optional OU filter: playbooks whose scope SET contains the unit
	// (Organization page deep link). Membership only — a global (unscoped)
	// playbook is available to every unit but is not "scoped to" any.
	var ouFilter *uuid.UUID
	if ouParam := c.QueryParam("ou_id"); ouParam != "" {
		ouID, err := uuid.Parse(ouParam)
		if err != nil {
			return results.SendValidationError(c, "invalid ou_id")
		}
		ouFilter = &ouID
	}

	query := workflow_template_list.WorkflowTemplateListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
		Name:        nameFilter,
		IsShortcut:  isShortcutFilter,
	}

	templates, err := h.templateService.GetAllByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list templates", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list templates", err)
	}

	// Convert to results. One permission resolve covers the page — the answer
	// is per-caller, not per-row.
	allowAuthoring := h.callerCanReadSecretValues(c, accountID)
	templateResults := make([]*results.WorkflowTemplateResult, 0, len(templates))
	for i := range templates {
		if ouFilter != nil && !templateScopeContains(&templates[i], *ouFilter) {
			continue
		}
		row := results.NewWorkflowTemplateResult(&templates[i])
		if !allowAuthoring {
			row.RedactAuthoring()
		}
		templateResults = append(templateResults, row)
	}

	return results.SendOKResponse(c, templateResults, "templates retrieved successfully")
}

// templateScopeContains reports whether the template's OU scope set names the
// unit.
func templateScopeContains(t *workflow_template.WorkflowTemplate, ouID uuid.UUID) bool {
	for _, id := range t.OUIDs {
		if id == ouID {
			return true
		}
	}
	return false
}

// UpdateTemplate updates an existing workflow template
// @Summary Update workflow template
// @Description Updates workflow template information
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Template ID"
// @Param template body workflow_template_update.WorkflowTemplateUpdateCommand true "Workflow template update data"
// @Success 200 {object} results.APIResponse{data=results.WorkflowTemplateResult} "Template updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Template not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates/{id} [put]
func (h *WorkflowHandler) UpdateTemplate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse template ID from URL parameter
	templateIDStr := c.Param("id")
	templateID, err := uuid.Parse(templateIDStr)
	if err != nil {
		h.logger.Warn("invalid template ID", "error", err, "template_id", templateIDStr)
		return results.SendValidationError(c, "invalid template ID")
	}

	var cmd workflow_template_update.WorkflowTemplateUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set ID and auth context
	cmd.ID = templateID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Same privileged-authoring gate as Create: block types are read from the
	// incoming payload so an update cannot INJECT privileged blocks past it.
	if err := h.authorizePrivilegedAuthoring(c, cmd.Graph, cmd.Steps, cmd.ScopeOUIDs()); err != nil {
		return err
	}

	// ...and again inside the service, against the MERGED document. The two are
	// not the same question and the pre-check alone is not enough: an omitted
	// graph means "unchanged", so this check sees an empty document while the
	// stored privileged graph survives — under whatever `ou_id` and `status`
	// this request carries. Without the second call a playbook.author holder
	// could re-scope and publish a privileged playbook whose content nothing
	// examined, which is precisely what the dedicated re-target route refuses.
	// The pre-check is kept because it refuses an injection before any read.
	template, err := h.templateService.Update(c.Request().Context(), cmd,
		func(graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep, ouIDs []uuid.UUID) error {
			return h.authorizePrivilegedAuthoring(c, graph, steps, ouIDs)
		})
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update template", err, "template_id", templateID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "template not found")
		}
		if errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
			return results.SendValidationError(c, err.Error())
		}
		// A stale precondition is the caller's to resolve: re-read and re-apply.
		// Answering 500 would tell them to retry the same losing write.
		if errors.Is(err, app_errors.ErrWorkflowTemplateStale) {
			return results.SendError(c, http.StatusConflict, err.Error(), nil)
		}
		return results.SendInternalServerError(c, "failed to update template", err)
	}

	templateResult := results.NewWorkflowTemplateResult(template)
	// A request that omitted the graph gets the STORED document back — that is
	// a read, and it redacts like one. A request that carried the graph only
	// gets its own submission echoed.
	if cmd.Graph == nil && !h.callerCanReadSecretValues(c, accountID) {
		templateResult.RedactAuthoring()
	}
	return results.SendOKResponse(c, templateResult, "template updated successfully")
}

// SetTemplateOU re-targets a playbook's organizational-unit scope
// @Summary Re-target a playbook to an organizational unit
// @Description Sets (or clears, with a null ou_id) the OU a playbook is scoped to, without rewriting the rest of the document
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Param id path string true "Template ID"
// @Security ApiKeyAuth
// @Param request body SetTemplateOURequest true "Target OU (null clears the scope)"
// @Success 200 {object} results.APIResponse{data=results.WorkflowTemplateResult} "Template re-targeted successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Template not found"
// @Router /api/v1/workflow-templates/{id}/organizational-unit [put]
func (h *WorkflowHandler) SetTemplateOU(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	templateID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid template ID")
	}

	var req SetTemplateOURequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	// ou_ids wins when present; ou_id remains for the client that sends one.
	var ouIDs []uuid.UUID
	if req.OUIDs != nil {
		ouIDs = make([]uuid.UUID, 0, len(*req.OUIDs))
		for _, raw := range *req.OUIDs {
			parsed, perr := uuid.Parse(raw)
			if perr != nil {
				return results.SendValidationError(c, "invalid organizational unit ID")
			}
			ouIDs = append(ouIDs, parsed)
		}
	} else if req.OUID != nil && *req.OUID != "" {
		parsed, perr := uuid.Parse(*req.OUID)
		if perr != nil {
			return results.SendValidationError(c, "invalid organizational unit ID")
		}
		ouIDs = []uuid.UUID{parsed}
	}

	template, err := h.templateService.SetOUs(c.Request().Context(), accountID, userID, templateID, ouIDs,
		func(graph *workflow_template.WorkflowGraph, steps []workflow_template.WorkflowStep, target []uuid.UUID) error {
			// Re-checked against the NEW scope with the STORED graph: the
			// privileged-authoring answer depends on both, and the stored graph
			// is the one that will actually run there.
			return h.authorizePrivilegedAuthoring(c, graph, steps, target)
		})
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to re-target template", err, "template_id", templateID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "template not found")
		}
		if errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
			return results.SendValidationError(c, err.Error())
		}
		if he, ok := err.(*echo.HTTPError); ok {
			return he
		}
		return results.SendInternalServerError(c, "failed to re-target template", err)
	}
	retargeted := results.NewWorkflowTemplateResult(template)
	// The caller submitted only a scope; the document in the response is the
	// stored one — a read.
	if !h.callerCanReadSecretValues(c, accountID) {
		retargeted.RedactAuthoring()
	}
	return results.SendOKResponse(c, retargeted, "template re-targeted successfully")
}

// SetTemplateOURequest is the body of the re-target route. A null ou_id clears
// the scope (the playbook becomes available to every node in the account).
type SetTemplateOURequest struct {
	// OUID is the legacy single-unit field; OUIDs supersedes it and wins when
	// both are present. An explicit empty ou_ids clears the scope, which ou_id
	// alone cannot express distinctly from "not supplied".
	OUID  *string   `json:"ou_id"`
	OUIDs *[]string `json:"ou_ids"`
}

// DeleteTemplate deletes a workflow template
// @Summary Delete workflow template
// @Description Deletes a workflow template
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Template ID"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Template not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates/{id} [delete]
func (h *WorkflowHandler) DeleteTemplate(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse template ID from URL parameter
	templateIDStr := c.Param("id")
	templateID, err := uuid.Parse(templateIDStr)
	if err != nil {
		h.logger.Warn("invalid template ID", "error", err, "template_id", templateIDStr)
		return results.SendValidationError(c, "invalid template ID")
	}

	err = h.templateService.Delete(c.Request().Context(), accountID, userID, templateID)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to delete template", err, "template_id", templateID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "template not found")
		}
		return results.SendInternalServerError(c, "failed to delete template", err)
	}

	return c.NoContent(http.StatusNoContent)
}

// ExportTemplate exports a workflow template as a portable playbook document
// @Summary Export workflow template
// @Description Exports a playbook as a portable JSON document (name, description, graph). Account-specific ids (OS images, SSH keys) are stripped; the response is the raw document, served as a file download
// @Tags workflow-templates
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Template ID"
// @Success 200 {object} workflow_template.PortableDocument "The portable playbook document itself as a JSON download (Content-Disposition: attachment) — bare, not wrapped in the results.APIResponse envelope"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Template not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates/{id}/export [get]
func (h *WorkflowHandler) ExportTemplate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	templateID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid template ID")
	}

	template, err := h.templateService.GetByID(c.Request().Context(), workflow_template_get.WorkflowTemplateGetQuery{
		ID:          templateID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get template for export", err, "template_id", templateID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "template not found")
		}
		return results.SendInternalServerError(c, "failed to export template", err)
	}

	doc := template.ExportPortable()
	// The export is a READ of the authored document: free-text params redact
	// for a caller without secret.read_value exactly as on the template
	// routes. Importing such a file is refused by Validate (the placeholder
	// check), which is honest — a playbook with its scripts hidden is not a
	// playbook anyone can run.
	if !h.callerCanReadSecretValues(c, accountID) {
		doc.Graph = results.RedactGraphAuthoring(doc.Graph)
	}
	// The document IS the payload (no API envelope): it round-trips through
	// the import endpoint and must stay a self-contained portable file.
	c.Response().Header().Set(echo.HeaderContentDisposition,
		fmt.Sprintf("attachment; filename=%q", exportFilename(template.Name)))
	return c.JSON(http.StatusOK, doc)
}

// exportFilename derives a safe download filename from a playbook name.
func exportFilename(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "playbook"
	}
	return slug + ".playbook.json"
}

// ImportTemplate imports a portable playbook document as a draft
// @Summary Import workflow template
// @Description Creates a draft playbook from a portable document (an exported playbook or a built-in starter). The graph is validated against the block registry; unresolvable account-scoped references are cleared and reported as warnings
// @Tags workflow-templates
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param import body workflow_template_import.WorkflowTemplateImportCommand true "Import payload: optional ou_id + portable playbook document"
// @Success 201 {object} results.APIResponse{data=results.WorkflowTemplateImportResult} "Draft created; warnings list unresolved references"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Privileged blocks require playbook.author_privileged"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-templates/import [post]
func (h *WorkflowHandler) ImportTemplate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd workflow_template_import.WorkflowTemplateImportCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind import request", "error", err)
		return results.SendValidationError(c, bindErrorMessage(err))
	}

	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// The same content-based gate as Create/Update: an imported document can
	// carry privileged blocks (run_script, install_os, secret_upsert, ...)
	// just as well as a hand-authored one.
	if err := h.authorizePrivilegedAuthoring(c, cmd.Document.Graph, nil, cmd.ScopeOUIDs()); err != nil {
		return err
	}

	template, warnings, err := h.templateService.Import(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to import template", err, "account_id", accountID, "name", cmd.Document.Name)
		if errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to import template", err)
	}

	importResult := results.NewWorkflowTemplateImportResult(template, warnings)
	return results.SendCreatedResponse(c, importResult, "template imported successfully")
}

// ListStarters returns the built-in starter playbook catalog
// @Summary List starter playbooks
// @Description Returns the built-in catalog of ready-made playbooks. Each entry carries the full portable document; instantiate one by posting its document to the import endpoint
// @Tags workflow-templates
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.WorkflowStarterResult} "Starter catalog"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/workflow-templates/starters [get]
func (h *WorkflowHandler) ListStarters(c echo.Context) error {
	starters := workflow_template.Starters()
	starterResults := make([]*results.WorkflowStarterResult, len(starters))
	for i, starter := range starters {
		starterResults[i] = results.NewWorkflowStarterResult(starter)
	}
	return results.SendOKResponse(c, starterResults, "starter playbooks retrieved successfully")
}

// StartExecution starts a workflow execution
// @Summary Start workflow execution
// @Description Starts a workflow execution on selected nodes
// @Tags workflow-executions
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param execution body workflow_execution_start.WorkflowExecutionStartCommand true "Workflow execution start data"
// @Success 201 {object} results.APIResponse{data=results.WorkflowExecutionResult} "Execution started successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "A target node is not in this account"
// @Failure 409 {object} results.APIResponse "Workflow template is not active"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions [post]
func (h *WorkflowHandler) StartExecution(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd workflow_execution_start.WorkflowExecutionStartCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Authorize the run by the highest-privilege block in the template against
	// the template's OU scope: a plain reboot/status playbook needs playbook.run
	// (manager+), one containing run_script/install_os/secret_upsert/etc. needs
	// playbook.run_privileged (admin+).
	template, err := h.templateService.GetByID(c.Request().Context(), workflow_template_get.WorkflowTemplateGetQuery{
		ID:          cmd.TemplateID,
		AuthContext: cmd.AuthContext,
	})
	if err != nil {
		return results.SendNotFoundError(c, "template not found")
	}
	runAction := requiredRunAction(template.BlockTypes(), authz.ActionPlaybookRun, authz.ActionPlaybookRunPrivileged)
	// Every unit of the scope must allow the run. This can only ever refuse
	// more than the single-unit check did, never less, which is the property
	// worth having while a scope grows from one unit to a set.
	//
	// The sharper rule is per TARGETED NODE — a user entitled in unit A should
	// be able to run an A+B playbook on A's nodes — and the node ids are in
	// hand here. It is deliberately not done in the same change as the data
	// model: it LOOSENS the check, and loosening is not something to slip in
	// beside a schema migration.
	for _, target := range runAuthorizationTargets(template) {
		if err := middleware.Authorize(c, h.enforcer, runAction, target); err != nil {
			return err
		}
	}

	execution, err := h.executionService.StartExecution(c.Request().Context(), cmd)
	if err != nil {
		if errors.Is(err, app_errors.ErrWorkflowTemplateNotActive) {
			return results.SendError(c, http.StatusConflict, "This playbook is a draft and cannot be run. Set it to active first.", err)
		}
		if errors.Is(err, app_errors.ErrForbidden) {
			return results.SendError(c, http.StatusForbidden, "A target node is outside the organizational units this playbook is scoped to.", err)
		}
		if errors.Is(err, app_errors.ErrTooManyActiveExecutions) {
			return results.SendError(c, http.StatusTooManyRequests, "This account has too many runs in progress. Wait for some to finish before starting another.", err)
		}
		if errors.Is(err, app_errors.ErrTooManyRunningNodes) {
			return results.SendError(c, http.StatusTooManyRequests,
				"This run would exceed the account's limit on nodes running playbook steps at once. Wait for runs in progress to finish, or target fewer nodes.", err)
		}
		if errors.Is(err, app_errors.ErrNoValidTargetNodes) {
			return results.SendValidationError(c, "None of the selected nodes were found in this account, so the run cannot start.")
		}
		// One target outside the account refuses the whole run, with the
		// answer the batch command route gives: a node of another account and
		// a node that does not exist are indistinguishable.
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		results.LogFailure(c.Request().Context(), h.logger, "failed to start execution", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to start execution", err)
	}

	// The run-start audit row is written by the service, which every start
	// passes through.

	executionResult := results.NewWorkflowExecutionResult(execution)
	// The snapshot pinned at start comes from the STORED template, not from
	// this request — a read, redacted like one.
	if !h.callerCanReadSecretValues(c, accountID) {
		executionResult.RedactAuthoring()
	}
	return results.SendCreatedResponse(c, executionResult, "execution started successfully")
}

// GetExecution retrieves a workflow execution by ID
// @Summary Get workflow execution by ID
// @Description Retrieves workflow execution details by ID
// @Tags workflow-executions
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Execution ID"
// @Success 200 {object} results.APIResponse{data=results.WorkflowExecutionResult} "Execution retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Execution not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions/{id} [get]
func (h *WorkflowHandler) GetExecution(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse execution ID from URL parameter
	executionIDStr := c.Param("id")
	executionID, err := uuid.Parse(executionIDStr)
	if err != nil {
		h.logger.Warn("invalid execution ID", "error", err, "execution_id", executionIDStr)
		return results.SendValidationError(c, "invalid execution ID")
	}

	query := workflow_execution_get.WorkflowExecutionGetQuery{
		ID:          executionID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	execution, err := h.executionService.GetByID(c.Request().Context(), query)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get execution", err, "execution_id", executionID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "execution not found")
		}
		return results.SendInternalServerError(c, "failed to get execution", err)
	}

	// Get execution nodes
	nodes, err := h.executionService.GetExecutionNodes(c.Request().Context(), executionID)
	if err != nil {
		h.logger.Warn("failed to get execution nodes", "error", err, "execution_id", executionID)
		// Continue without nodes
	}

	executionResult := results.NewWorkflowExecutionResult(execution)
	redact := !h.callerCanReadSecretValues(c, accountID)
	if redact {
		// The pinned snapshot is the authored document: literal credentials
		// typed into free-text params redact here exactly as they do on the
		// template routes.
		executionResult.RedactAuthoring()
	}
	if len(nodes) > 0 {
		if !redact {
			h.recordRunSecretRead(c, accountID)
		}
		// The pinned graph, so the redaction can also hide which branch a
		// secret-derived condition took — a fact its block statuses spell out.
		graph := runGraph(execution)
		nodeResults := make([]results.WorkflowExecutionNodeResult, len(nodes))
		for i := range nodes {
			nodeResults[i] = *results.NewWorkflowExecutionNodeResultForRun(&nodes[i], redact, graph)
		}
		executionResult.Nodes = nodeResults
	}
	// Node count is already stored in execution, use it directly
	executionResult.NodeCount = execution.NodeCount

	return results.SendOKResponse(c, executionResult, "execution retrieved successfully")
}

// ListExecutions retrieves one page of workflow executions for the account
// @Summary List workflow executions
// @Description Retrieves a paginated list of workflow executions (newest first) for the authenticated account
// @Tags workflow-executions
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param template_id query string false "Filter by playbook (template) ID"
// @Param ou_id query string false "Filter by organizational unit: runs targeting at least one node in the OU's subtree"
// @Param page query int false "Page number (1-based, default 1)"
// @Param per_page query int false "Page size (default 50, max 200)"
// @Success 200 {object} results.APIResponse{data=results.WorkflowExecutionListResult} "Executions retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions [get]
func (h *WorkflowHandler) ListExecutions(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse query parameters for filtering
	var templateIDFilter *uuid.UUID
	if templateIDParam := c.QueryParam("template_id"); templateIDParam != "" {
		templateID, err := uuid.Parse(templateIDParam)
		if err == nil {
			templateIDFilter = &templateID
		}
	}
	var ouIDFilter *uuid.UUID
	if ouIDParam := c.QueryParam("ou_id"); ouIDParam != "" {
		ouID, err := uuid.Parse(ouIDParam)
		if err == nil {
			ouIDFilter = &ouID
		}
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))

	query := workflow_execution_list.WorkflowExecutionListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
		TemplateID:  templateIDFilter,
		OUID:        ouIDFilter,
		Page:        page,
		PerPage:     perPage,
	}
	// Normalize HERE (the service revalidates its own copy): the response
	// envelope must echo the EFFECTIVE page/per_page, not the raw input —
	// otherwise per_page=0/1000 echoes back and breaks consumers' paging math.
	if err := query.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	executions, total, err := h.executionService.List(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list executions", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list executions", err)
	}

	// Convert to results (node count is already stored in execution). One
	// permission resolve covers the page.
	allowAuthoring := h.callerCanReadSecretValues(c, accountID)
	executionResults := make([]*results.WorkflowExecutionResult, len(executions))
	for i := range executions {
		executionResults[i] = results.NewWorkflowExecutionResult(&executions[i])
		executionResults[i].NodeCount = executions[i].NodeCount
		if !allowAuthoring {
			executionResults[i].RedactAuthoring()
		}
	}

	listResult := results.NewWorkflowExecutionListResult(executionResults, total, query.Page, query.PerPage)
	return results.SendOKResponse(c, listResult, "executions retrieved successfully")
}

// GetExecutionNodes retrieves all execution nodes for a workflow execution
// @Summary Get workflow execution nodes
// @Description Retrieves all execution nodes for a workflow execution
// @Tags workflow-executions
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Execution ID"
// @Success 200 {object} results.APIResponse{data=[]results.WorkflowExecutionNodeResult} "Execution nodes retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Execution not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions/{id}/nodes [get]
func (h *WorkflowHandler) GetExecutionNodes(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse execution ID from URL parameter
	executionIDStr := c.Param("id")
	executionID, err := uuid.Parse(executionIDStr)
	if err != nil {
		h.logger.Warn("invalid execution ID", "error", err, "execution_id", executionIDStr)
		return results.SendValidationError(c, "invalid execution ID")
	}

	// Verify execution exists and belongs to account
	query := workflow_execution_get.WorkflowExecutionGetQuery{
		ID:          executionID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	execution, err := h.executionService.GetByID(c.Request().Context(), query)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get execution", err, "execution_id", executionID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "execution not found")
		}
		return results.SendInternalServerError(c, "failed to get execution", err)
	}

	// Get execution nodes
	nodes, err := h.executionService.GetExecutionNodes(c.Request().Context(), executionID)
	if err != nil {
		h.logger.Error("failed to get execution nodes", "error", err, "execution_id", executionID)
		return results.SendInternalServerError(c, "failed to get execution nodes", err)
	}

	// Convert to results. Block outputs carry whatever read_file/run_script
	// returned, so they need the same gate as a command payload.
	redact := !h.callerCanReadSecretValues(c, accountID)
	if !redact {
		h.recordRunSecretRead(c, accountID)
	}
	graph := runGraph(execution)
	nodeResults := make([]*results.WorkflowExecutionNodeResult, len(nodes))
	for i := range nodes {
		nodeResults[i] = results.NewWorkflowExecutionNodeResultForRun(&nodes[i], redact, graph)
	}

	return results.SendOKResponse(c, nodeResults, "execution nodes retrieved successfully")
}

// runAuthorizationTargets is the list of OU targets a run must be authorized
// against: one per scoped unit, or a single nil (account-wide) when unscoped.
func runAuthorizationTargets(template *workflow_template.WorkflowTemplate) []*uuid.UUID {
	if !template.IsScoped() {
		return []*uuid.UUID{nil}
	}
	out := make([]*uuid.UUID, 0, len(template.OUIDs))
	for i := range template.OUIDs {
		ouID := template.OUIDs[i]
		out = append(out, &ouID)
	}
	return out
}

// runGraph returns the graph a run was pinned to. Runs created before
// snapshotting existed carry none, and the redaction then falls back to
// field-level stripping — the same behaviour those runs have always had.
func runGraph(execution *workflow_execution.WorkflowExecution) *workflow_template.WorkflowGraph {
	if execution == nil || execution.TemplateSnapshot == nil {
		return nil
	}
	return execution.TemplateSnapshot.Graph
}

// callerCanReadSecretValues reports whether the request's user may see resolved
// secret material. Fails CLOSED on any error resolving the caller: an
// unidentifiable requester is not a privileged one.
//
// It only ANSWERS. Recording is recordRunSecretRead, called by each route after
// its read succeeded — the two were one function once, and the recording then
// fired at permission-check time: a request for a nonexistent execution wrote
// "plaintext served" and got a 404, which is the one lie the trail must not
// tell.
func (h *WorkflowHandler) callerCanReadSecretValues(c echo.Context, accountID uuid.UUID) bool {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return false
	}
	return h.enforcer.Can(c.Request().Context(), accountID, userID, authz.ActionSecretReadValue, nil)
}

// recordRunSecretRead writes the audit row for a run view that actually served
// resolved secret material. A run's block outputs and command history hold
// whatever the engine substituted a {{secrets.*}} reference into and whatever
// the device printed back, so replaying a finished run is a read of the values
// themselves. Called just before the 2xx, never before the read.
func (h *WorkflowHandler) recordRunSecretRead(c echo.Context, accountID uuid.UUID) {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return
	}
	h.audit.RecordSecretValuesRead(c.Request().Context(), audit.UserActor(accountID, userID),
		audit.ScopeRunState, audit_event.TargetPlaybookExecution, c.Param("id"))
}

// GetExecutionNodeCommands retrieves all commands for a workflow execution node
// @Summary Get commands for execution node
// @Description Retrieves all commands executed for a specific node in a workflow execution, ordered by created_at
// @Tags workflow-executions
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Execution ID"
// @Param nodeId path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=[]results.NodeCommandResult} "Commands retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Execution node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions/{id}/nodes/{nodeId}/commands [get]
func (h *WorkflowHandler) GetExecutionNodeCommands(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse execution ID from URL parameter
	executionIDStr := c.Param("id")
	executionID, err := uuid.Parse(executionIDStr)
	if err != nil {
		h.logger.Warn("invalid execution ID", "error", err, "execution_id", executionIDStr)
		return results.SendValidationError(c, "invalid execution ID")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("nodeId")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// Resolved BEFORE the read: the same permission decides whether payloads are
	// redacted and whether the commands of hidden blocks are listed at all. The
	// audit row is written AFTER it, once the read has succeeded — an event
	// claiming plaintext was served, followed by a 404, is the one lie the
	// trail must not tell.
	allowSecrets := h.callerCanReadSecretValues(c, accountID)

	// Get commands for the execution node
	commands, err := h.executionService.GetExecutionNodeCommands(c.Request().Context(), executionID, nodeID, accountID, allowSecrets)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get execution node commands", err, "execution_id", executionID, "node_id", nodeID)
		if errors.Is(err, app_errors.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			return results.SendNotFoundError(c, "execution node not found")
		}
		return results.SendInternalServerError(c, "failed to get execution node commands", err)
	}
	if allowSecrets {
		h.recordRunSecretRead(c, accountID)
	}

	// Redact resolved secret material for callers lacking secret.read_value: the
	// workflow engine persists {{secrets.*}} into the script/content/value payload
	// fields, so without this a viewer/manager could read plaintext secrets from a
	// privileged playbook's command history (the node command endpoints already
	// redact — this endpoint did not). Fail closed on any error.
	// Convert to results
	commandResults := make([]results.NodeCommandResult, len(commands))
	for i, cmd := range commands {
		// Get workflow_execution_node_id for each command
		var workflowExecutionNodeID *string
		execNodeID, err := h.executionService.GetWorkflowExecutionNodeIDForCommand(c.Request().Context(), cmd.ID)
		if err == nil && execNodeID != nil {
			execNodeIDStr := execNodeID.String()
			workflowExecutionNodeID = &execNodeIDStr
		}

		payload := results.PublicCommandPayload(cmd.Payload, allowSecrets)
		commandResults[i] = results.NodeCommandResult{
			ID:                      cmd.ID.String(),
			NodeID:                  cmd.NodeID.String(),
			AccountID:               cmd.AccountID.String(),
			Slug:                    cmd.Slug,
			Payload:                 payload,
			Status:                  cmd.Status.String(),
			Progress:                cmd.Progress,
			Details:                 results.PublicCommandDetails(cmd.Details, cmd.Payload, allowSecrets),
			Result:                  results.PublicCommandResult(cmd.Result, allowSecrets),
			WorkflowExecutionNodeID: workflowExecutionNodeID,
			CreatedAt:               cmd.CreatedAt,
			UpdatedAt:               cmd.UpdatedAt,
		}
	}

	return results.SendOKResponse(c, commandResults, "commands retrieved successfully")
}

// CancelExecution settles a run that is stuck, freeing the per-account active
// cap it consumes.
//
// This is the operator escape hatch. Before it, a run
// pinned active — by a deleted node, a dropped enqueue, or a block whose agent
// never answered — had NO remedy: no DELETE, no force-complete, and the run kept
// counting against WORKFLOW_MAX_ACTIVE_EXECUTIONS_PER_ACCOUNT while retention
// (which keys on completed_at) never swept it. Enough of those end playbooks
// for the account with nothing to call.
//
// @Summary Cancel a workflow execution
// @Description Settles a running or stuck execution: every unfinished node is failed, its in-flight device commands are settled, and the run is completed so it stops counting against the account's active-execution cap. Idempotent — cancelling a finished run changes nothing.
// @Tags workflow-executions
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Execution ID"
// @Success 200 {object} results.APIResponse{data=map[string]int} "Cancelled; settled = number of execution nodes failed"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Execution not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/workflow-executions/{id}/cancel [post]
func (h *WorkflowHandler) CancelExecution(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	executionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid execution ID")
	}

	settled, err := h.executionService.CancelExecution(c.Request().Context(), accountID, executionID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "execution not found")
		}
		results.LogFailure(c.Request().Context(), h.logger, "failed to cancel execution", err, "execution_id", executionID)
		return results.SendInternalServerError(c, "failed to cancel execution", err)
	}

	// Recorded only for a cancel that reached a run of this account; the
	// settled count says whether it stopped anything.
	userID, _ := middleware.GetUserIDFromContext(c)
	h.audit.RecordDetail(c.Request().Context(), audit.UserActor(accountID, userID),
		audit_event.ActionPlaybookRunCancel, audit_event.TargetPlaybookExecution, executionID.String(),
		fmt.Sprintf("settled=%d", settled))

	return results.SendOKResponse(c, map[string]int{"settled": settled}, "execution cancelled")
}
