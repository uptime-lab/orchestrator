package node

import (
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Adoption-refusal route. Registered from RegisterRoutes as a STATIC segment
// before the `:id` routes, like /nodes/deleted and /nodes/favorites.
//
// Gated on node.create and nothing narrower: a refused device belongs to no OU
// yet — that is the whole problem — so there is no unit to scope it to, and
// node.create is exactly the right the Add Node flow itself requires. No new
// action: whoever may start an adoption may learn why one is not finishing.
func (h *NodeHandler) registerAdoptionRefusalRoutes(nodes *echo.Group) {
	nodes.GET("/adoption-refusals", h.ListAdoptionRefusals,
		middleware.RequireAction(h.enforcer, authz.ActionNodeCreate))
}

// ListAdoptionRefusals lists devices this account could not admit
// @Summary List refused adoptions
// @Description Returns the devices this account's install flow has turned away because their hardware serial is registered to another account. The device itself only ever receives a deliberately vague refusal and keeps retrying, so without this the person waiting on Add Node sees nothing. Derived from the platform's refusal record at read time: `live` says the device was refused within the last few minutes (still powered on and retrying), `still_bound_elsewhere` says the serial is held by another account right now. Names the serial only, never the owning account.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.AdoptionRefusalListResult} "Refused adoptions retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Router /api/v1/nodes/adoption-refusals [get]
func (h *NodeHandler) ListAdoptionRefusals(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	refusals, err := h.nodeService.ListAdoptionRefusals(c.Request().Context(), accountID)
	if err != nil {
		h.logger.Error("failed to list adoption refusals", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list refused adoptions", err)
	}
	return results.SendOKResponse(c, results.NewAdoptionRefusalListResult(refusals), "refused adoptions retrieved successfully")
}
