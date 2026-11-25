package node

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// canReadSecretValues reports whether the caller may see resolved secret
// material in command payloads. On any error it fails CLOSED (redact).
//
// A YES is RECORDED. A command payload holds what the engine substituted a
// {{secrets.*}} reference into, and a prior run_script result holds whatever the
// device printed — replaying either is a read of the value itself, and it left
// no row.
func (h *NodeHandler) canReadSecretValues(c echo.Context, accountID uuid.UUID, subjectID string) bool {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return false
	}
	if !h.enforcer.Can(c.Request().Context(), accountID, userID, authz.ActionSecretReadValue, nil) {
		return false
	}
	h.audit.RecordSecretValuesRead(c.Request().Context(), audit.UserActor(accountID, userID),
		audit.ScopeCommandPayload, audit_event.TargetNode, subjectID)
	return true
}

// GetNodeCommands lists node commands for a node
// @Summary List node commands
// @Description Lists all commands for a specific node with pagination
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param limit query int false "Limit (default: 50)"
// @Param offset query int false "Offset (default: 0)"
// @Success 200 {object} results.APIResponse{data=results.NodeCommandListResult} "List of node commands"
// @Failure 400 {object} results.APIResponse "Invalid node ID"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/node-commands [get]
func (h *NodeHandler) GetNodeCommands(c echo.Context) error {
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}

	// Get account ID from context
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// OU-scoped read: don't expose command history of a node in an OU the caller
	// cannot read (mirrors the mutating paths).
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr
	}

	// Parse pagination parameters
	limit := 50 // default
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 100 {
			limit = parsedLimit
		}
	}

	offset := 0 // default
	if offsetStr := c.QueryParam("offset"); offsetStr != "" {
		if parsedOffset, err := strconv.Atoi(offsetStr); err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	commands, err := h.nodeCommandService.GetNodeCommandsByNodeID(c.Request().Context(), accountID, nodeID, limit, offset)
	if err != nil {
		h.logger.Error("failed to get node commands", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to get node commands", err)
	}

	// Convert to result format
	allowSecrets := h.canReadSecretValues(c, accountID, c.Param("id"))
	if !allowSecrets {
		if commands, err = h.visibleRunCommands(c.Request().Context(), accountID, commands); err != nil {
			return results.SendInternalServerError(c, "failed to get node commands", err)
		}
	}
	commandResults := make([]results.NodeCommandResult, len(commands))
	for i, cmd := range commands {
		payload := results.PublicCommandPayload(cmd.Payload, allowSecrets)
		commandResults[i] = results.NodeCommandResult{
			ID:        cmd.ID.String(),
			NodeID:    cmd.NodeID.String(),
			AccountID: cmd.AccountID.String(),
			Slug:      cmd.Slug,
			Payload:   payload,
			Status:    cmd.Status.String(),
			Progress:  cmd.Progress,
			Details:   results.PublicCommandDetails(cmd.Details, cmd.Payload, allowSecrets),
			Result:    results.PublicCommandResult(cmd.Result, allowSecrets),
			CreatedAt: cmd.CreatedAt,
			UpdatedAt: cmd.UpdatedAt,
		}
	}

	result := results.NodeCommandListResult{
		Commands: commandResults,
		Total:    len(commandResults), // Total count (could be enhanced to return actual total from DB)
		Limit:    limit,
		Offset:   offset,
	}

	return results.SendOKResponse(c, result, "node commands retrieved successfully")
}

// GetNodeCommand gets a node command by ID
// @Summary Get node command
// @Description Gets a specific node command by ID
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node Command ID"
// @Success 200 {object} results.APIResponse{data=results.NodeCommandResult} "Node command"
// @Failure 400 {object} results.APIResponse "Invalid command ID"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Command not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/node-commands/{id} [get]
func (h *NodeHandler) GetNodeCommand(c echo.Context) error {
	commandIDStr := c.Param("id")
	commandID, err := uuid.Parse(commandIDStr)
	if err != nil {
		return results.SendValidationError(c, "invalid command ID")
	}

	// Get account ID from context
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	cmd, err := h.nodeCommandService.GetNodeCommandByID(c.Request().Context(), accountID, commandID)
	if err != nil {
		h.logger.Error("failed to get node command", "error", err, "command_id", commandID)
		return results.SendNotFoundError(c, "command not found")
	}

	// OU-scoped read, resolved through the command's node. The sibling
	// GetNodeCommands does this on the way in because it is given a node ID;
	// here the node is only known after the lookup, which is why the check was
	// missing. Account scoping alone left intra-account cross-OU reads open.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, cmd.NodeID); aerr != nil {
		return aerr
	}

	// The read is recorded against the command's NODE: the route's id is the
	// command's, and a row naming it as a node matches no node a reviewer
	// would filter on.
	allowSecrets := h.canReadSecretValues(c, accountID, cmd.NodeID.String())
	if !allowSecrets {
		visible, err := h.runCommandVisible(c.Request().Context(), accountID, cmd)
		if err != nil {
			return results.SendInternalServerError(c, "failed to get node command", err)
		}
		if !visible {
			// Withheld exactly like a command that does not exist.
			return results.SendNotFoundError(c, "command not found")
		}
	}
	payload := results.PublicCommandPayload(cmd.Payload, allowSecrets)
	result := results.NodeCommandResult{
		ID:        cmd.ID.String(),
		NodeID:    cmd.NodeID.String(),
		AccountID: cmd.AccountID.String(),
		Slug:      cmd.Slug,
		Payload:   payload,
		Status:    cmd.Status.String(),
		Progress:  cmd.Progress,
		Details:   results.PublicCommandDetails(cmd.Details, cmd.Payload, allowSecrets),
		Result:    results.PublicCommandResult(cmd.Result, allowSecrets),
		CreatedAt: cmd.CreatedAt,
		UpdatedAt: cmd.UpdatedAt,
	}

	return results.SendOKResponse(c, result, "node command retrieved successfully")
}
