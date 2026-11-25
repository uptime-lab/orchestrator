package node

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Deleted-node routes. Registered from RegisterRoutes with the STATIC segment
// before the `:id` ones, the same way /nodes/favorites is: Echo prefers a static
// segment over a param, but only if it is registered as one.
//
// Authorization is account-level, not OU-scoped, and that is forced rather than
// chosen: the node row is gone, so there is no OU to scope against. All three
// carry node.delete — restoring a device onto the account and forgetting it
// forever are both consequences of the delete, and gating them behind a lesser
// right would let someone undo (or make permanent) an action they could not have
// taken.
func (h *NodeHandler) registerDeletedNodeRoutes(nodes *echo.Group) {
	nodes.GET("/deleted", h.ListDeleted,
		middleware.RequireAction(h.enforcer, authz.ActionNodeDelete))
	nodes.POST("/deleted/:id/restore", h.RestoreDeleted,
		middleware.RequireAction(h.enforcer, authz.ActionNodeDelete))
	nodes.DELETE("/deleted/:id", h.ForgetDeleted,
		middleware.RequireAction(h.enforcer, authz.ActionNodeDelete))
}

// ListDeleted lists nodes that were deleted and whose device can be taken back
// @Summary List deleted nodes
// @Description Returns the account's deleted-node records. Deleting a node removes the row but does not tell the device, which keeps retrying with an identity the platform no longer knows; these records are what a restore uses. The unique token is never returned. A record whose serial is now carried by another live node of this account names that node in serial_bound_in_account and is not restorable.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.DeletedNodeListResult} "Deleted nodes retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Router /api/v1/nodes/deleted [get]
func (h *NodeHandler) ListDeleted(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	tombstones, err := h.nodeService.ListDeleted(c.Request().Context(), accountID)
	if err != nil {
		h.logger.Error("failed to list deleted nodes", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list deleted nodes", err)
	}
	return results.SendOKResponse(c, results.NewDeletedNodeListResult(tombstones), "deleted nodes retrieved successfully")
}

// RestoreDeleted re-creates a deleted node under its original identity
// @Summary Restore a deleted node
// @Description Re-creates the node row under its ORIGINAL id, token and boot override, so a device still carrying that identity is accepted again. Restores an identity only: command history, tracked files, secret attachments and workflow runs are gone for good. The device re-reports its hardware on the next connect. Refused with 409 when the node's hardware serial has since been registered to another account: a deleted node's serial is free for anyone to bind, and restoring one that has moved would return a record no device can reach. Refused with 409 (code serial_bound_in_account, the live node's node_id and node_name in data) when the serial is carried by another live node of this account — the board was adopted again as a new node; delete that node first to take this one back. A node that was admitted when it was deleted is admitted again by its restore, under the device cap: when the install is at its cap the restore is refused with 409 (code device_cap_reached, the numbers in data) and the record stays restorable; likewise when the account is at its own device limit (code account_device_limit_reached; data: limit, account_devices, plan, until for a dated subscription, contact).
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Deleted node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node restored successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "No deleted-node record"
// @Failure 409 {object} results.APIResponse "Already restored, a live node holds that id, the hardware serial is now registered to another account, the hardware serial is another live node of this account (code serial_bound_in_account), the install is at its device cap (code device_cap_reached), the account is at its own device limit (code account_device_limit_reached), or the node's stored credential cannot be read (code node_credential_unreadable: restore the encryption key, or forget the record and adopt the device again)"
// @Failure 503 {object} results.APIResponse "The device cap could not be checked (code device_cap_unverified): nothing was written; try again"
// @Router /api/v1/nodes/deleted/{id}/restore [post]
func (h *NodeHandler) RestoreDeleted(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}

	restored, err := h.nodeService.RestoreDeleted(c.Request().Context(), accountID, actorUserID, nodeID)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrNotFound):
			return results.SendNotFoundError(c, "no deleted-node record for this id")
		case errors.Is(err, app_errors.ErrNodeAlreadyRestored),
			errors.Is(err, app_errors.ErrNodeAlreadyExists):
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		case errors.As(err, new(*entitlement.DeviceCapError)),
			errors.As(err, new(*entitlement.AccountDeviceLimitError)),
			errors.As(err, new(*entitlement.DeviceCapUnverifiedError)):
			// A refusal at the cap is not a fault: answered as its typed 409,
			// not logged as an error. One because the cap could not be
			// checked is answered as its typed 503; the service logged it.
			return results.SendInternalServerError(c, "failed to restore the node", err)
		default:
			results.LogFailure(c.Request().Context(), h.logger, "failed to restore a deleted node", err, "node_id", nodeID)
			return results.SendInternalServerError(c, "failed to restore the node", err)
		}
	}
	return results.SendOKResponse(c, results.NewNodeResult(restored), "node restored successfully")
}

// ForgetDeleted drops a deleted-node record
// @Summary Forget a deleted node
// @Description Drops the deleted-node record. After this the device cannot be taken back without physical access — which is the point: while the record exists, anyone who can delete a node can also bring that device back onto the account.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Deleted node ID"
// @Success 204 "Record forgotten"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "No deleted-node record"
// @Router /api/v1/nodes/deleted/{id} [delete]
func (h *NodeHandler) ForgetDeleted(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.nodeService.ForgetDeleted(c.Request().Context(), accountID, actorUserID, nodeID); err != nil {
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "no deleted-node record for this id")
		}
		h.logger.Error("failed to forget a deleted node", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to forget the record", err)
	}
	return results.SendNoContentResponse(c)
}
