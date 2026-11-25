// Package utility serves the Utilities API (docs/UTILITIES-PLAN.md): the
// static catalog, assignment CRUD, the per-node effective view and the apply
// endpoints. The apply slug is deliberately absent from the generic command
// mapping — these routes are its only entry.
package utility

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Handler serves the utilities API.
type Handler struct {
	svc      *utility_service.Service
	logger   *slog.Logger
	enforcer *authz_service.Enforcer
}

func NewHandler(svc *utility_service.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger.With("handler", "utility")}
}

// RegisterRoutes wires the utilities endpoints. The catalog is authed but
// action-free (the GET /workflow-blocks precedent: a static registry,
// identical for every tenant, no account in the query); everything touching
// tenant rows carries utility.read or utility.manage.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	// Authorization is IN-HANDLER, against the target's own scope.
	// The account-level RequireAction gate these routes used to carry demands
	// an account-wide binding for a nil target, which locked an OU-scoped
	// member out of the whole surface — while docs/UTILITIES-PLAN.md §3.8
	// promises OU-scoped holders can operate within their scope. utility.read
	// is in authz.anyScopeReadActions (the door), the list handlers narrow via
	// AllowedOUs (the walls), and every per-target route authorizes against
	// the unit(s) the target actually touches. Mutations keep the strict rule:
	// account-wide scope still needs an account-wide binding, and an OU-set
	// scope needs the action on EVERY unit (the workflow-templates precedent).
	h.enforcer = enforcer

	g := e.Group("/api/v1/utilities")
	g.Use(authMiddleware)
	g.GET("/catalog", h.Catalog)
	g.GET("", h.List)
	g.GET("/ou-counts", h.OUCounts)
	g.POST("/assignments", h.CreateAssignment)
	g.GET("/assignments/:id", h.GetAssignment)
	g.PUT("/assignments/:id", h.UpdateAssignment)
	g.DELETE("/assignments/:id", h.DeleteAssignment)
	g.POST("/assignments/:id/apply", h.ApplyAssignment)

	e.GET("/api/v1/nodes/:id/effective-utilities", h.EffectiveForNode, authMiddleware)
	e.GET("/api/v1/organizational-units/:id/utility-ladder", h.LadderForOU, authMiddleware)
	e.POST("/api/v1/nodes/:id/utilities/apply", h.ApplyToNode, authMiddleware)
}

// requireAccountWide is the strict nil-target check. middleware.Authorize with
// a nil target would take the any-scope branch for utility.read, so routes
// where "account-wide holders only" is the rule (an account-wide assignment,
// a node in no unit) must ask AllowedOUs directly.
func (h *Handler) requireAccountWide(c echo.Context, action authz.Action) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return echo.NewHTTPError(401, "unauthorized")
	}
	allowAll, _, err := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, action)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	if !allowAll {
		return echo.NewHTTPError(403, "forbidden: insufficient permissions")
	}
	return nil
}

// authorizeScope authorizes an action against an assignment-shaped scope.
//
// write=true is the mutation rule: an OU set demands the action on EVERY unit,
// because an assignment spanning two units is a change to both. write=false is
// the availability rule: reading an assignment that touches YOUR unit is
// allowed even when it also touches another (the playbook-availability
// precedent — scope is a union).
func (h *Handler) authorizeScope(c echo.Context, action authz.Action, accountWide bool, ouIDs []uuid.UUID, nodeID *uuid.UUID, write bool) error {
	switch {
	case len(ouIDs) > 0:
		if write {
			for i := range ouIDs {
				if err := middleware.Authorize(c, h.enforcer, action, &ouIDs[i]); err != nil {
					return err
				}
			}
			return nil
		}
		var last error
		for i := range ouIDs {
			if last = middleware.Authorize(c, h.enforcer, action, &ouIDs[i]); last == nil {
				return nil
			}
		}
		return last
	case nodeID != nil:
		accountID, _, err := ids(c)
		if err != nil {
			return echo.NewHTTPError(401, "unauthorized")
		}
		ou, err := h.svc.NodeOU(c.Request().Context(), accountID, *nodeID)
		if err != nil {
			return echo.NewHTTPError(404, "node not found")
		}
		if ou == nil {
			// A node in no unit is an OU-less resource: account-wide only.
			return h.requireAccountWide(c, action)
		}
		return middleware.Authorize(c, h.enforcer, action, ou)
	default:
		// Account-wide, or unassigned. Both are account-level state.
		return h.requireAccountWide(c, action)
	}
}

// LadderForOU godoc
// @Summary Utility rollup for a unit
// @Description Per-utility "applied on X of Y nodes" across a unit and its child units. Y is how many of those nodes the utility RESOLVES to, which is not the unit's node count: a nearer assignment on a node overrides the unit's, and a child unit can resolve a different set. An empty list means no utility reaches any node of the unit.
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Organizational unit ID"
// @Success 200 {object} results.APIResponse{data=[]results.OUUtilityLadderResult}
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/utility-ladder [get]
func (h *Handler) LadderForOU(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ouID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit id")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionUtilityRead, &ouID); err != nil {
		return err
	}
	rows, err := h.svc.LadderForOU(c.Request().Context(), accountID, ouID)
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "failed to roll up the utility ladder for a unit",
			"error", err, "ou_id", ouID)
		return results.SendInternalServerError(c, "failed to load the utility rollup", err)
	}
	return results.SendOKResponse(c, results.NewOUUtilityLadderResults(rows), "utility rollup")
}

// Catalog godoc
// @Summary Utility catalog
// @Description Returns the static utility registry: what exists, its consent class, dependencies and param schemas (workflow-block shape, rendered by the same generated forms).
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.UtilityDefinitionResult}
// @Failure 401 {object} results.APIResponse
// @Router /api/v1/utilities/catalog [get]
func (h *Handler) Catalog(c echo.Context) error {
	return results.SendOKResponse(c,
		results.NewUtilityDefinitionResults(h.svc.Catalog().Utilities), "utility catalog")
}

// List godoc
// @Summary List utility assignments
// @Description Lists every utility assignment of the account (one utility × one target × one config per row).
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.UtilityAssignmentResult}
// @Failure 401 {object} results.APIResponse
// @Router /api/v1/utilities [get]
func (h *Handler) List(c echo.Context) error {
	if err := middleware.Authorize(c, h.enforcer, authz.ActionUtilityRead, nil); err != nil {
		return err
	}
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	allowAll, allowed, err := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, authz.ActionUtilityRead)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	list, err := h.svc.ListAssignmentsForScope(c.Request().Context(), accountID, allowAll, allowed)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list utility assignments", err)
	}
	return results.SendOKResponse(c, results.NewUtilityAssignmentResults(list), "utility assignments")
}

// OUCounts godoc
// @Summary Per-OU utility assignment counts
// @Description Returns how many utility assignments target each organizational unit — the OU rail facet.
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]int64}
// @Failure 401 {object} results.APIResponse
// @Router /api/v1/utilities/ou-counts [get]
func (h *Handler) OUCounts(c echo.Context) error {
	if err := middleware.Authorize(c, h.enforcer, authz.ActionUtilityRead, nil); err != nil {
		return err
	}
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	allowAll, allowed, err := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, authz.ActionUtilityRead)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	counts, err := h.svc.OUCounts(c.Request().Context(), accountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to count utility assignments", err)
	}
	out := make(map[string]int64, len(counts))
	for ou, n := range counts {
		if !allowAll && !allowed[ou] {
			continue
		}
		out[ou.String()] = n
	}
	return results.SendOKResponse(c, out, "utility assignment counts")
}

// assignmentRequest is the create body. Scope: exactly one of account_wide /
// ou_ids / node_id, or none (unassigned — applies to nothing until targeted).
// The legacy single ou_id is still accepted and merged into the set.
type assignmentRequest struct {
	UtilityID   string                 `json:"utility_id"`
	AccountWide bool                   `json:"account_wide"`
	OUIDs       []uuid.UUID            `json:"ou_ids"`
	OUID        *uuid.UUID             `json:"ou_id"`
	NodeID      *uuid.UUID             `json:"node_id"`
	Config      map[string]interface{} `json:"config"`
	Enabled     *bool                  `json:"enabled"`
}

// updateRequest is the update body. Omitted fields stay unchanged; the scope
// is replaced only when retarget is true (a config edit must not silently
// re-scope — the full-document-PUT lesson).
type updateRequest struct {
	Config      map[string]interface{} `json:"config"`
	Enabled     *bool                  `json:"enabled"`
	Retarget    bool                   `json:"retarget"`
	AccountWide bool                   `json:"account_wide"`
	OUIDs       []uuid.UUID            `json:"ou_ids"`
	OUID        *uuid.UUID             `json:"ou_id"`
	NodeID      *uuid.UUID             `json:"node_id"`
}

// mergedOUIDs folds the legacy single ou_id into the set (the access-entry
// helper, same name on purpose).
func mergedOUIDs(ouIDs []uuid.UUID, ouID *uuid.UUID) []uuid.UUID {
	if ouID != nil {
		for _, existing := range ouIDs {
			if existing == *ouID {
				return ouIDs
			}
		}
		ouIDs = append(ouIDs, *ouID)
	}
	return ouIDs
}

// CreateAssignment godoc
// @Summary Create a utility assignment
// @Description Assigns a utility to an OU, a node, the whole account, or leaves it unassigned. Creating an assignment dispatches nothing — apply is explicit.
// @Tags utilities
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body assignmentRequest true "Assignment"
// @Success 201 {object} results.APIResponse{data=results.UtilityAssignmentResult}
// @Failure 400 {object} results.APIResponse
// @Failure 401 {object} results.APIResponse
// @Failure 409 {object} results.APIResponse
// @Router /api/v1/utilities/assignments [post]
func (h *Handler) CreateAssignment(c echo.Context) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req assignmentRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	if err := h.authorizeScope(c, authz.ActionUtilityManage, req.AccountWide, mergedOUIDs(req.OUIDs, req.OUID), req.NodeID, true); err != nil {
		return err
	}
	a, err := h.svc.CreateAssignment(c.Request().Context(), accountID, userID, utility_service.AssignmentInput{
		UtilityID:   req.UtilityID,
		AccountWide: req.AccountWide,
		OUIDs:       mergedOUIDs(req.OUIDs, req.OUID),
		NodeID:      req.NodeID,
		Config:      req.Config,
		Enabled:     req.Enabled,
	})
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendCreatedResponse(c, results.NewUtilityAssignmentResult(a), "utility assignment created")
}

// GetAssignment godoc
// @Summary Get a utility assignment
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Assignment ID"
// @Success 200 {object} results.APIResponse{data=results.UtilityAssignmentResult}
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/utilities/assignments/{id} [get]
func (h *Handler) GetAssignment(c echo.Context) error {
	accountID, _, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid assignment id")
	}
	a, err := h.svc.GetAssignment(c.Request().Context(), accountID, id)
	if err != nil {
		return h.mapError(c, err)
	}
	if err := h.authorizeScope(c, authz.ActionUtilityRead, a.AccountWide, a.OUIDs, a.NodeID, false); err != nil {
		return err
	}
	return results.SendOKResponse(c, results.NewUtilityAssignmentResult(a), "utility assignment")
}

// UpdateAssignment godoc
// @Summary Update a utility assignment
// @Description Edits config/enabled; replaces the scope only when retarget=true. Validation runs on the merged row.
// @Tags utilities
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Assignment ID"
// @Param body body updateRequest true "Changes"
// @Success 200 {object} results.APIResponse{data=results.UtilityAssignmentResult}
// @Failure 400 {object} results.APIResponse
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Failure 409 {object} results.APIResponse
// @Router /api/v1/utilities/assignments/{id} [put]
func (h *Handler) UpdateAssignment(c echo.Context) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid assignment id")
	}
	var req updateRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	existing, err := h.svc.GetAssignment(c.Request().Context(), accountID, id)
	if err != nil {
		return h.mapError(c, err)
	}
	if err := h.authorizeScope(c, authz.ActionUtilityManage, existing.AccountWide, existing.OUIDs, existing.NodeID, true); err != nil {
		return err
	}
	if req.Retarget {
		// Moving an assignment needs rights on BOTH ends — the workflow
		// re-target precedent: dragging out of your scope into somebody
		// else's is a change to theirs.
		if err := h.authorizeScope(c, authz.ActionUtilityManage, req.AccountWide, mergedOUIDs(req.OUIDs, req.OUID), req.NodeID, true); err != nil {
			return err
		}
	}
	a, err := h.svc.UpdateAssignment(c.Request().Context(), accountID, userID, id, utility_service.AssignmentUpdate{
		Config:      req.Config,
		Enabled:     req.Enabled,
		Retarget:    req.Retarget,
		AccountWide: req.AccountWide,
		OUIDs:       mergedOUIDs(req.OUIDs, req.OUID),
		NodeID:      req.NodeID,
	})
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewUtilityAssignmentResult(a), "utility assignment updated")
}

// DeleteAssignment godoc
// @Summary Delete a utility assignment
// @Description Removes the assignment row. Unassign-is-retain: nothing is dispatched and the software stays on devices, unmanaged.
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Assignment ID"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/utilities/assignments/{id} [delete]
func (h *Handler) DeleteAssignment(c echo.Context) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid assignment id")
	}
	existing, err := h.svc.GetAssignment(c.Request().Context(), accountID, id)
	if err != nil {
		return h.mapError(c, err)
	}
	if err := h.authorizeScope(c, authz.ActionUtilityManage, existing.AccountWide, existing.OUIDs, existing.NodeID, true); err != nil {
		return err
	}
	if err := h.svc.DeleteAssignment(c.Request().Context(), accountID, userID, id); err != nil {
		return h.mapError(c, err)
	}
	return results.SendNoContentResponse(c)
}

// ApplyAssignment godoc
// @Summary Apply an assignment to every node it reaches
// @Description Dispatches the wholesale utility apply to every node the assignment reaches, bounded by the per-account concurrent-applies cap. Nodes past the bound are reported deferred, never silently dropped.
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Assignment ID"
// @Success 200 {object} results.APIResponse{data=[]results.UtilityApplyResult}
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/utilities/assignments/{id}/apply [post]
func (h *Handler) ApplyAssignment(c echo.Context) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid assignment id")
	}
	existing, err := h.svc.GetAssignment(c.Request().Context(), accountID, id)
	if err != nil {
		return h.mapError(c, err)
	}
	if err := h.authorizeScope(c, authz.ActionUtilityManage, existing.AccountWide, existing.OUIDs, existing.NodeID, true); err != nil {
		return err
	}
	res, err := h.svc.ApplyAssignment(c.Request().Context(), accountID, userID, id)
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewUtilityApplyResults(res), "utility apply dispatched")
}

// EffectiveForNode godoc
// @Summary Effective utilities of a node
// @Description Returns the node's resolved utility set with provenance (node/OU/account) and the per-utility ladder status.
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=[]results.EffectiveUtilityResult}
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/nodes/{id}/effective-utilities [get]
func (h *Handler) EffectiveForNode(c echo.Context) error {
	accountID, _, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	if err := h.authorizeScope(c, authz.ActionUtilityRead, false, nil, &nodeID, false); err != nil {
		return err
	}
	state, err := h.svc.EffectiveForNode(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewEffectiveUtilityResults(state), "effective utilities")
}

// ApplyToNode godoc
// @Summary Apply a node's resolved utility set
// @Description Resolves the node's utility set and dispatches ONE wholesale apply_utility_config command. An empty set dispatches nothing (unassign-is-retain).
// @Tags utilities
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.UtilityApplyResult}
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/nodes/{id}/utilities/apply [post]
func (h *Handler) ApplyToNode(c echo.Context) error {
	accountID, userID, err := ids(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	if err := h.authorizeScope(c, authz.ActionUtilityManage, false, nil, &nodeID, true); err != nil {
		return err
	}
	res, err := h.svc.ApplyToNode(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return h.mapError(c, err)
	}
	out := results.NewUtilityApplyResults([]utility_service.NodeApplyResult{*res})
	return results.SendOKResponse(c, out[0], "utility apply dispatched")
}

func ids(c echo.Context) (accountID, userID uuid.UUID, err error) {
	accountID, err = middleware.GetAccountIDFromContext(c)
	if err != nil {
		return
	}
	userID, err = middleware.GetUserIDFromContext(c)
	return
}

// mapError converts domain errors to statuses. Typed refusals (the assignment
// cap) declare their own status through OperatorFacingError and are answered
// as themselves by SendInternalServerError.
func (h *Handler) mapError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, utility.ErrUnknownUtility),
		errors.Is(err, utility.ErrScopeConflict),
		errors.Is(err, utility.ErrInvalidConfig),
		errors.Is(err, utility.ErrDependencyMissing):
		return results.SendValidationError(c, err.Error())
	// A dangling target reference (an OU deleted between pick and save, or a
	// garbage node id) is the CALLER's input being wrong, not a platform
	// failure — it used to answer 500. The repositories return
	// app_errors sentinels, not gorm.ErrRecordNotFound, so both are matched.
	case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound),
		errors.Is(err, app_errors.ErrNodeNotFound):
		return results.SendValidationError(c, err.Error())
	case errors.Is(err, utility.ErrDuplicateAssignment):
		return results.SendErrorWithCode(c, 409, err.Error(), "utility_assignment_duplicate")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return results.SendNotFoundError(c, "utility assignment not found")
	default:
		return results.SendInternalServerError(c, "utility operation failed", err)
	}
}
