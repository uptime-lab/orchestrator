package node_favorite

import (
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	node_favorite_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_favorite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// NodeFavoriteHandler serves per-user node favorites.
type NodeFavoriteHandler struct {
	service *node_favorite_service.Service
	logger  *slog.Logger
}

func NewNodeFavoriteHandler(service *node_favorite_service.Service, logger *slog.Logger) *NodeFavoriteHandler {
	return &NodeFavoriteHandler{service: service, logger: logger.With("handler", "node_favorite")}
}

func (h *NodeFavoriteHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	nodes := e.Group("/api/v1/nodes")
	nodes.Use(authMiddleware)
	// The static segment wins over the :id wildcard in Echo's router.
	//
	// Only the write carries the action. A favorite is a purely personal view
	// with no policy semantics, and the list returns the CALLER's own marks —
	// gating that would mean a member could be denied the ability to read a
	// preference only they can see. ActionFavoriteSet was declared, returned by
	// /users/me/permissions and present in the role matrix with no enforcement
	// site anywhere; this is it.
	nodes.GET("/favorites", h.ListFavorites)
	nodes.PUT("/:id/favorite", h.SetFavorite,
		middleware.RequireAction(enforcer, authz.ActionFavoriteSet))
}

// ListFavorites returns the caller's favorite node ids
// @Summary List favorite nodes
// @Description Returns the ids of the nodes the authenticated user marked as favorite
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.NodeFavoriteListResult} "Favorites retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/favorites [get]
func (h *NodeFavoriteHandler) ListFavorites(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ids, err := h.service.ListFavoriteNodeIDs(c.Request().Context(), accountID, userID)
	if err != nil {
		h.logger.Error("failed to list favorite nodes", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to list favorite nodes", err)
	}
	return results.SendOKResponse(c, results.NewNodeFavoriteListResult(ids), "favorites retrieved successfully")
}

// favoriteRequest is the PUT body — a named type so the document carries the
// real schema.
type favoriteRequest struct {
	Favorite bool `json:"favorite"`
}

// SetFavorite marks or unmarks a node as the caller's favorite
// @Summary Set node favorite
// @Description Marks or unmarks a node as the authenticated user's favorite
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param favorite body favoriteRequest true "Favorite flag"
// @Success 200 {object} results.APIResponse{data=results.NodeFavoriteListResult} "Favorites updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/favorite [put]
func (h *NodeFavoriteHandler) SetFavorite(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	var body favoriteRequest
	if err := c.Bind(&body); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if err := h.service.SetFavorite(c.Request().Context(), accountID, userID, nodeID, body.Favorite); err != nil {
		h.logger.Error("failed to set node favorite", "error", err, "node_id", nodeID, "user_id", userID)
		return results.SendNotFoundError(c, "node not found")
	}
	ids, err := h.service.ListFavoriteNodeIDs(c.Request().Context(), accountID, userID)
	if err != nil {
		h.logger.Error("failed to list favorite nodes", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to list favorite nodes", err)
	}
	return results.SendOKResponse(c, results.NewNodeFavoriteListResult(ids), "favorites updated successfully")
}
