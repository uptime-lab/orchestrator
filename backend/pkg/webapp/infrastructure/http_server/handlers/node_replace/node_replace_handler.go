package node_replace

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	node_replace_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_replace"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// NodeReplaceHandler serves the "replace node with another" flow.
type NodeReplaceHandler struct {
	service *node_replace_service.Service
	logger  *slog.Logger
}

func NewNodeReplaceHandler(service *node_replace_service.Service, logger *slog.Logger) *NodeReplaceHandler {
	return &NodeReplaceHandler{service: service, logger: logger.With("handler", "node_replace")}
}

func (h *NodeReplaceHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	nodes := e.Group("/api/v1/nodes")
	nodes.Use(authMiddleware)
	// Replace transfers identity and can run a playbook: admin+. Checked
	// account-wide (the flow spans two nodes' scopes).
	nodes.POST("/:id/replace", h.Replace, middleware.RequireAction(enforcer, authz.ActionNodeReplace))
}

type replaceRequest struct {
	TargetNodeID uuid.UUID  `json:"target_node_id"`
	TemplateID   *uuid.UUID `json:"template_id,omitempty"`
}

// Replace transfers a failed node's identity onto a spare node.
// @Summary Replace a node with another
// @Description Transfers the source node's swappable identity onto the target: clones its custom attributes, carries the hostname as an attribute, transfers OU membership, optionally runs a playbook on the target to re-apply the identity to external services, then strips the source's attributes (the source node record is kept).
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Source (failed) node ID"
// @Param body body replaceRequest true "Target node and optional playbook"
// @Success 200 {object} results.APIResponse{data=node_replace_service.Result} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "The target would be outside the playbook's organizational units; nothing was transferred"
// @Failure 404 {object} results.APIResponse "Node or playbook not found"
// @Failure 409 {object} results.APIResponse "The playbook is a draft; nothing was transferred"
// @Failure 429 {object} results.APIResponse "A run cap is reached; nothing was transferred"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/replace [post]
func (h *NodeReplaceHandler) Replace(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	sourceID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	var req replaceRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if req.TargetNodeID == uuid.Nil {
		return results.SendValidationError(c, "target_node_id is required")
	}

	auth := commands.NewAuthContext(accountID, userID)
	result, err := h.service.Replace(c.Request().Context(), auth, sourceID, req.TargetNodeID, req.TemplateID)
	if err != nil {
		return sendReplaceError(c, err)
	}
	return results.SendOKResponse(c, result, "Node replaced successfully")
}

func sendReplaceError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, node_replace_service.ErrSameNode):
		return results.SendValidationError(c, err.Error())
	case errors.Is(err, node_replace_service.ErrPlaybookRefused):
		return sendPlaybookRefused(c, err)
	case errors.Is(err, app_errors.ErrNodeNotFound):
		return results.SendNotFoundError(c, "node not found")
	default:
		return results.SendInternalServerError(c, "failed to replace node", err)
	}
}

// sendPlaybookRefused answers the playbook start's own refusals as the runs
// route answers them. They are asked before the identity moves, so nothing
// was transferred.
func sendPlaybookRefused(c echo.Context, err error) error {
	switch {
	case errors.Is(err, app_errors.ErrNotFound):
		return results.SendNotFoundError(c, "playbook not found")
	case errors.Is(err, app_errors.ErrWorkflowTemplateNotActive):
		return results.SendError(c, http.StatusConflict, "This playbook is a draft and cannot be run. Set it to active first.", err)
	case errors.Is(err, app_errors.ErrForbidden):
		return results.SendError(c, http.StatusForbidden, "The replacement node would be outside the organizational units this playbook is scoped to.", err)
	case errors.Is(err, app_errors.ErrTooManyActiveExecutions):
		return results.SendError(c, http.StatusTooManyRequests, "This account has too many runs in progress. Wait for some to finish before starting another.", err)
	case errors.Is(err, app_errors.ErrTooManyRunningNodes):
		return results.SendError(c, http.StatusTooManyRequests,
			"This run would exceed the account's limit on nodes running playbook steps at once. Wait for runs in progress to finish.", err)
	case errors.Is(err, app_errors.ErrNodeNotFound):
		return results.SendNotFoundError(c, "node not found")
	default:
		return results.SendInternalServerError(c, "failed to replace node", err)
	}
}
