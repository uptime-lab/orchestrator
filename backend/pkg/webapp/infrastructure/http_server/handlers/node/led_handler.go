package node

import (
	"errors"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// GetLED returns the node's LED state.
// @Summary Get LED configuration
// @Description Desired LED behaviour (stealth mode, ACT/PWR disable, per-LED ambient indication), what the device confirmed (applied hash, in_sync), whether the converge is still queued, the newest converge command, and what the node's board makes possible (capabilities).
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=nodeDomain.LEDState} "LED state"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/led-config [get]
func (h *NodeHandler) GetLED(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr
	}
	state, err := h.nodeService.GetLED(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapLEDError(c, err)
	}
	return results.SendOKResponse(c, state, "led state retrieved")
}

// PutLED stores and dispatches a desired LED configuration.
// @Summary Set LED configuration
// @Description Validates the desired LED behaviour against the node's board (stealth and digital indication need a board that declares them), stores it, and dispatches the converge command. The agent persists the config and re-applies it on boot.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body nodeDomain.LEDConfig true "Desired LED behaviour" SchemaExample({"stealth_mode": false, "act_led_off": true, "pwr_led_off": true, "digital_sources": ["temperature", "load"]})
// @Success 200 {object} results.APIResponse{data=nodeDomain.LEDState} "LED state (queued=true means accepted but not applied yet)"
// @Failure 400 {object} results.APIResponse "Validation error, or the board cannot honor the config"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/led-config [put]
func (h *NodeHandler) PutLED(c echo.Context) error {
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
		return results.SendValidationError(c, "invalid node ID")
	}
	// LED behaviour is a device property edit (nothing destructive), so it
	// carries node.update rather than the watchdog's file_write tier.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeUpdate, nodeID); aerr != nil {
		return aerr
	}

	var cfg nodeDomain.LEDConfig
	if err := c.Bind(&cfg); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}
	state, err := h.nodeService.SetLED(c.Request().Context(), accountID, userID, nodeID, cfg)
	if err != nil {
		return h.mapLEDError(c, err)
	}
	return results.SendOKResponse(c, state, "led configuration dispatched")
}

// DeleteLED removes the node's own LED configuration.
// @Summary Reset LED configuration
// @Description Clears the node's own LED configuration, so the node follows its unit's (or the account's) LED policy again, and dispatches the converge to that policy; with no policy anywhere, to the defaults (stealth off, LEDs restored, indication stopped).
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=nodeDomain.LEDState} "LED state after the reset dispatch"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/led-config [delete]
func (h *NodeHandler) DeleteLED(c echo.Context) error {
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
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeUpdate, nodeID); aerr != nil {
		return aerr
	}
	state, err := h.nodeService.ResetLED(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return h.mapLEDError(c, err)
	}
	return results.SendOKResponse(c, state, "led reset dispatched")
}

// mapLEDError maps LED service errors onto HTTP statuses. A typed refusal
// answers as itself; the two operator-worded validation shapes (board gate,
// config bounds) pass through as 400; everything else is a platform failure
// and answers a GENERIC 500 — a catch-all used to echo raw
// repository/DB error text to the client.
func (h *NodeHandler) mapLEDError(c echo.Context, err error) error {
	var facing results.OperatorFacingError
	switch {
	case errors.As(err, &facing):
		return results.SendInternalServerError(c, "led operation failed", err)
	case errors.Is(err, app_errors.ErrNodeNotFound), errors.Is(err, echo.ErrNotFound):
		return results.SendNotFoundError(c, "node not found")
	case errors.Is(err, nodeDomain.ErrLEDNotSupported), errors.Is(err, nodeDomain.ErrLEDInvalid):
		return results.SendValidationError(c, err.Error())
	default:
		return results.SendInternalServerError(c, "failed to apply led configuration", err)
	}
}
