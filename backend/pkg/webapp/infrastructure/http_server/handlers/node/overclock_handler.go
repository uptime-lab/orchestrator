package node

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Overclock presets: the catalog lives in the node domain
// (domain/node/overclock.go) and is applied over the EXISTING patch_file
// marker-block command — no new agent command. The platform never reboots the
// node for an overclock change: config.txt is read by the firmware at boot
// only, so the operator reboots explicitly (and the boot-loop failsafe covers
// an overclock that fails to boot).

// overclockApplyRequest is the POST /nodes/:id/overclock body.
type overclockApplyRequest struct {
	Preset string `json:"preset"`
}

// GetOverclockPresets returns the overclock presets applicable to this node
// @Summary Get overclock presets for a node
// @Description Returns the overclock preset catalog for the node's detected board (Pi 4 / CM4 / Pi 5 / CM5). Board is empty and presets are empty when the node's model has no catalog.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.OverclockPresetsResult} "Presets retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/overclock-presets [get]
func (h *NodeHandler) GetOverclockPresets(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}

	// Viewing the catalog is a read; applying is gated separately below.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr
	}

	foundNode, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), nodeID, accountID)
	if err != nil {
		return results.SendNotFoundError(c, "node not found")
	}

	board, ok := foundNode.OverclockBoard()
	if !ok {
		// Not an error: the UI uses the empty catalog to hide the picker for
		// boards (or non-Pi hardware) we ship no presets for.
		return results.SendOKResponse(c, results.NewOverclockPresetsResult("", nil),
			"no overclock presets for this device model")
	}

	result := results.NewOverclockPresetsResult(board, nodeDomain.OverclockPresetsForBoard(board))
	return results.SendOKResponse(c, result, "overclock presets retrieved successfully")
}

// ApplyOverclockPreset applies an overclock preset to the node's config.txt
// @Summary Apply an overclock preset
// @Description Resolves the named preset for the node's detected board and dispatches a patch_file command that writes the preset's directives into a managed marker block in /boot/firmware/config.txt. The change takes effect only after the operator reboots the node — the platform NEVER reboots automatically. Use preset "stock" to revert (removes the overclock directives).
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body overclockApplyRequest true "Preset selection (preset id from the node catalog; stock reverts)"
// @Success 200 {object} results.APIResponse{data=results.OverclockApplyResult} "Preset dispatched successfully (reboot required to apply)"
// @Failure 400 {object} results.APIResponse "Validation error (unknown preset or unsupported board)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 503 {object} results.APIResponse "Node is not connected"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/overclock [post]
func (h *NodeHandler) ApplyOverclockPreset(c echo.Context) error {
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

	var req overclockApplyRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}
	req.Preset = strings.TrimSpace(req.Preset)
	if req.Preset == "" {
		return results.SendValidationError(c, "preset is required")
	}

	// Same gate as every other config.txt write dispatched from the UI
	// (patch_file maps to node.file_write in authz.ActionForCommand).
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	foundNode, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), nodeID, accountID)
	if err != nil {
		return results.SendNotFoundError(c, "node not found")
	}

	board, ok := foundNode.OverclockBoard()
	if !ok {
		return results.SendValidationError(c, nodeDomain.ErrOverclockUnsupportedBoard.Error())
	}

	preset, err := nodeDomain.ResolveOverclockPreset(board, req.Preset)
	if err != nil {
		if errors.Is(err, nodeDomain.ErrOverclockUnknownPreset) || errors.Is(err, nodeDomain.ErrOverclockUnsupportedBoard) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to resolve overclock preset", err)
	}

	// Reuse the patch_file dispatch path end-to-end (validation, queueing for
	// offline nodes, agent version floor, command history) instead of a
	// parallel channel. The dedicated marker keeps the overclock block
	// separate from other managed blocks (e.g. the watchdog one).
	cmd := node_command.NodeCommandCommand{
		NodeID:  nodeID,
		Command: node_command_domain.SlugPatchFile,
		Params: map[string]interface{}{
			"path":    nodeDomain.OverclockConfigPath,
			"marker":  nodeDomain.OverclockMarker,
			"content": preset.ConfigContent(),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	}
	if err := h.nodeService.SendCommand(c.Request().Context(), cmd); err != nil {
		h.logger.Error("failed to dispatch overclock preset", "error", err,
			"node_id", nodeID, "account_id", accountID, "preset", preset.Name, "board", board)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		if strings.Contains(err.Error(), "not connected") {
			return results.SendErrorWithCode(c, http.StatusServiceUnavailable, "node is not connected", "service_unavailable")
		}
		return results.SendInternalServerError(c, "failed to send overclock command", err)
	}

	return results.SendOKResponse(c, &results.OverclockApplyResult{
		Board:          string(board),
		Preset:         preset.Name,
		RebootRequired: true,
	}, "overclock preset dispatched — reboot the node to apply")
}
