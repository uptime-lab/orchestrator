package node

import (
	"errors"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// nodeFileMaxBytes is the per-version size cap. It lives in the domain and is
// deliberately NOT configurable: it bounds a gRPC frame, not storage, and a
// frame over the limit tears down the node's stream instead of failing the
// command. See node_file.MaxContentBytes.
const nodeFileMaxBytes = node_file.MaxContentBytes

// trackedFileLimit is the configured per-node file cap (0 = unlimited),
// falling back to the shipped default when no config is wired.
func (h *NodeHandler) trackedFileLimit() int {
	if h.config == nil {
		return node_file.DefaultLimits().FilesPerNode
	}
	return h.config.NodeFile.MaxFilesPerNode
}

// trackFileRequest is the body of the track and refresh calls.
type trackFileRequest struct {
	Path string `json:"path"`
}

// deleteOnDeviceRequest carries the typed confirmation. It is verified on the
// server: a destructive verb must not depend on the browser to ask twice.
type deleteOnDeviceRequest struct {
	Path        string `json:"path"`
	ConfirmPath string `json:"confirm_path"`
	Backup      bool   `json:"backup"`
}

// mapFileError turns domain file errors into HTTP responses.
func (h *NodeHandler) mapFileError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, node_file.ErrInvalidPath), errors.Is(err, node_file.ErrRefusedPath):
		return results.SendValidationError(c, err.Error())
	case errors.Is(err, node_file.ErrProtectedPath), errors.Is(err, node_file.ErrAccessManagedPath):
		return results.SendForbiddenError(c, err.Error())
	case errors.Is(err, app_errors.ErrNodeNotFound):
		return results.SendNotFoundError(c, "node not found")
	case errors.Is(err, node_file.ErrNotTracked):
		return results.SendNotFoundError(c, "file is not tracked for this node")
	case errors.Is(err, node_file.ErrAlreadyTracked):
		return results.SendValidationError(c, "this file is already tracked")
	case errors.Is(err, node_file.ErrTrackedLimit):
		return results.SendValidationError(c, "this node already tracks the maximum number of files")
	default:
		var tooOld *node_service.AgentTooOldError
		if errors.As(err, &tooOld) {
			// A version floor is a precondition, not a server fault: the operator
			// needs to see "update the agent", not a 500.
			return results.SendValidationError(c, tooOld.Error())
		}
		return results.SendInternalServerError(c, "file operation failed", err)
	}
}

// TrackFile starts tracking a file: it dispatches a bounded read and returns the
// command id to poll.
// @Summary Track a file from a node
// @Description Reads the file from the device and starts following it (versioned copies, edit, apply). The answer carries the read's command_id. The platform records the file when the device's read arrives, whether or not anyone polls; GET /api/v1/nodes/{id}/files/read/{commandId}?path=… reports the outcome (and the version) for a client that wants to wait for it.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body trackFileRequest true "Path to track"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Read dispatched"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/files/track [post]
func (h *NodeHandler) TrackFile(c echo.Context) error {
	return h.dispatchFileRead(c)
}

// RefreshFile re-reads a tracked file from the device.
// @Summary Refresh a tracked file
// @Description Re-reads the file from the device; a changed file becomes a new version. Like track, the result is recorded when the device's read arrives; the read endpoint reports the outcome.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body trackFileRequest true "Path to refresh"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Read dispatched"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/files/refresh [post]
func (h *NodeHandler) RefreshFile(c echo.Context) error {
	return h.dispatchFileRead(c)
}

func (h *NodeHandler) dispatchFileRead(c echo.Context) error {
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
	// Reading an arbitrary file off a device is a root-grade read; it carries
	// the same action as writing one, which is also what read_file already maps
	// to for workflow blocks.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	var req trackFileRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}

	commandID, err := h.nodeService.ReadNodeFile(c.Request().Context(), accountID, userID, nodeID, req.Path, nodeFileMaxBytes)
	if err != nil {
		return h.mapFileError(c, err)
	}
	return results.SendOKResponse(c, map[string]string{
		"command_id": commandID.String(),
		"path":       req.Path,
	}, "reading the file from the device")
}

// GetFileReadResult polls a dispatched track/refresh read.
// @Summary Poll a tracked-file read
// @Description Returns the state of a track/refresh read: running, completed (with the version, and changed when this read added it) or failed with a detail. The platform records the content when the device's read arrives; polling is not required for the file to be tracked, and a poll stores nothing twice.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param commandId path string true "Command ID"
// @Param path query string true "File path"
// @Success 200 {object} results.APIResponse{data=node_file.ReadResult} "Read state"
// @Failure 404 {object} results.APIResponse "Unknown command for this node"
// @Router /api/v1/nodes/{id}/files/read/{commandId} [get]
func (h *NodeHandler) GetFileReadResult(c echo.Context) error {
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
	commandID, err := uuid.Parse(c.Param("commandId"))
	if err != nil {
		return results.SendValidationError(c, "invalid command ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	out, err := h.nodeService.GetNodeFileReadResult(
		c.Request().Context(), accountID, userID, nodeID, commandID,
		c.QueryParam("path"), nodeFileMaxBytes, h.trackedFileLimit(),
	)
	if err != nil {
		return h.mapFileError(c, err)
	}
	return results.SendOKResponse(c, out, "read state retrieved")
}

// UntrackFile stops following a file. The device is untouched.
// @Summary Untrack a file
// @Description Removes the file and its version history from the platform; the file on the device is left alone
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body trackFileRequest true "Path to untrack"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Untracked"
// @Failure 403 {object} results.APIResponse "File is managed by the platform"
// @Router /api/v1/nodes/{id}/files/untrack [post]
func (h *NodeHandler) UntrackFile(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	var req trackFileRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}
	if err := h.nodeService.UntrackNodeFile(c.Request().Context(), accountID, nodeID, req.Path); err != nil {
		return h.mapFileError(c, err)
	}
	return results.SendOKResponse(c, map[string]string{"path": req.Path}, "file untracked")
}

// DeleteFileOnDevice removes the file from the device.
// @Summary Delete a tracked file on the device
// @Description Removes the file from the node itself. Requires the path to be repeated as confirmation. Protected files are refused.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body deleteOnDeviceRequest true "Delete request (the path repeated as confirm_path; backup keeps a .bak on the device)"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Delete dispatched"
// @Failure 400 {object} results.APIResponse "Confirmation does not match"
// @Failure 403 {object} results.APIResponse "File is managed by the platform"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/files/delete-on-device [post]
func (h *NodeHandler) DeleteFileOnDevice(c echo.Context) error {
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
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	var req deleteOnDeviceRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}

	commandID, err := h.nodeService.DeleteNodeFileOnDevice(
		c.Request().Context(), accountID, userID, nodeID, req.Path, req.ConfirmPath, req.Backup,
	)
	if err != nil {
		return h.mapFileError(c, err)
	}
	return results.SendOKResponse(c, map[string]string{
		"command_id": commandID.String(),
		"path":       req.Path,
	}, "deleting the file on the device")
}
