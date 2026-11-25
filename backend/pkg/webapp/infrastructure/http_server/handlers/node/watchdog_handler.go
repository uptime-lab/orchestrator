package node

import (
	"errors"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// watchdogRequest is the PUT body: either a named preset or explicit fields.
// The preset exists because the useful configurations are few and the dangerous
// ones are easy to type by hand.
type watchdogRequest struct {
	Preset     string `json:"preset,omitempty"`
	PingTarget string `json:"ping_target,omitempty"`
	nodeDomain.WatchdogConfig
}

// GetWatchdog returns the node's watchdog state.
// @Summary Get watchdog configuration
// @Description Desired watchdog supervision, what the device confirmed (applied hash, in_sync), whether the converge is still queued, the newest converge command (last_command), and the device's own facts: a breaker disarm (disarm_reason) or a pre-arm refusal (last_refusal). recovery_limited flags an enabled config on a node where a watchdog reboot loop has no netboot failsafe behind it.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=nodeDomain.WatchdogState} "Watchdog state"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/watchdog [get]
func (h *NodeHandler) GetWatchdog(c echo.Context) error {
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

	state, err := h.nodeService.GetWatchdog(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapWatchdogError(c, err)
	}
	// Only an existing, enabled config can be "armed with nothing behind it";
	// a never-configured or disabled node must not carry the flag.
	enabled := state.Desired != nil && state.Desired.Enabled
	state.RecoveryLimited = h.watchdogRecoveryLimited(c, accountID, nodeID, enabled)
	return results.SendOKResponse(c, state, "watchdog state retrieved")
}

// PutWatchdog stores and dispatches a desired watchdog configuration.
// @Summary Set watchdog configuration
// @Description Validates and stores the desired supervision, then dispatches the converge command. The agent refuses on-device any config that would immediately reboot-loop the node.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body watchdogRequest true "Either a named preset ({\"preset\": \"agent-health\"}, network preset also takes ping_target) or a full WatchdogConfig body"
// @Success 200 {object} results.APIResponse{data=nodeDomain.WatchdogState} "Watchdog state (queued=true means accepted but not applied yet)"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/watchdog [put]
func (h *NodeHandler) PutWatchdog(c echo.Context) error {
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
	// Arming a watchdog can reboot the device, so it carries the same action as
	// writing a node file rather than a read-level one.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); aerr != nil {
		return aerr
	}

	var req watchdogRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}

	cfg := req.WatchdogConfig
	if req.Preset != "" {
		preset, perr := nodeDomain.WatchdogPreset(req.Preset, req.PingTarget)
		if perr != nil {
			return results.SendValidationError(c, perr.Error())
		}
		cfg = preset
	}

	state, err := h.nodeService.SetWatchdog(c.Request().Context(), accountID, userID, nodeID, cfg)
	if err != nil {
		return h.mapWatchdogError(c, err)
	}
	state.RecoveryLimited = h.watchdogRecoveryLimited(c, accountID, nodeID, cfg.Enabled)
	return results.SendOKResponse(c, state, "watchdog configuration dispatched")
}

// DeleteWatchdog disarms the watchdog, keeping the stored configuration so it
// can be re-enabled without retyping it.
// @Summary Disarm the watchdog
// @Description Disables supervision on the device; the stored configuration is kept for a later re-enable
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=nodeDomain.WatchdogState} "Watchdog state after the disarm dispatch"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/watchdog [delete]
func (h *NodeHandler) DeleteWatchdog(c echo.Context) error {
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

	current, err := h.nodeService.GetWatchdog(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapWatchdogError(c, err)
	}
	cfg := nodeDomain.WatchdogConfig{}
	if current.Desired != nil {
		cfg = *current.Desired
	}
	cfg.Enabled = false

	state, err := h.nodeService.SetWatchdog(c.Request().Context(), accountID, userID, nodeID, cfg)
	if err != nil {
		return h.mapWatchdogError(c, err)
	}
	return results.SendOKResponse(c, state, "watchdog disarm dispatched")
}

// GetWatchdogPresets returns the watchdog preset catalog.
// @Summary Get the watchdog preset catalog
// @Description The four supervision presets with the operator wording that belongs with them (label, description, and the consequence sentence a confirmation must show) and the configuration each expands to. The network preset carries requires_ping_target and an empty ping_targets: it needs an address before it can be armed, and its consequence text carries the literal placeholder {{target}} for that address.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.WatchdogPresetsResult} "Preset catalog"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/watchdog-presets [get]
func (h *NodeHandler) GetWatchdogPresets(c echo.Context) error {
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	// Reading the catalog is a read; arming is gated separately on PutWatchdog.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr
	}
	return results.SendOKResponse(c,
		results.NewWatchdogPresetsResult(nodeDomain.WatchdogPresetCatalog()),
		"watchdog presets retrieved")
}

// ScanWatchdogStatus dispatches a live watchdog probe to a node.
// @Summary Probe the live watchdog status on a node
// @Description Dispatches a fixed read-only script that reports whether watchdog.service is actually running on the device, whether /dev/watchdog attached, and what the agent's breaker state says. Answers what the stored state cannot: a watchdog stopped by hand on the device. Returns a command id to poll. Online-only.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Probe dispatched; command_id to poll"
// @Failure 400 {object} results.APIResponse "Node offline"
// @Failure 403 {object} results.APIResponse "Requires node.status_check"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/watchdog/status-scan [post]
func (h *NodeHandler) ScanWatchdogStatus(c echo.Context) error {
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
	// Read-only on the device, but it still DISPATCHES a command: the agent runs
	// a script and the node flips to `maintenance` while it does. node.read is
	// the viewer's action, and a role documented as read-only must not cause
	// device-side execution — so this carries the "poke the device to tell me
	// its state" action instead, like every other dispatching probe.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeStatusCheck, nodeID); aerr != nil {
		return aerr
	}
	commandID, err := h.nodeService.ScanWatchdogStatus(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	return results.SendOKResponse(c, map[string]string{"command_id": commandID.String()}, "watchdog status probe dispatched")
}

// GetWatchdogStatusScan returns the parsed result of a dispatched live probe.
// @Summary Poll a live watchdog probe
// @Description Returns the parsed device-side watchdog status once the agent reports (status running/completed/failed). Raw script output is parsed server-side and never returned.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param commandId path string true "Probe command ID"
// @Success 200 {object} results.APIResponse{data=nodeDomain.WatchdogStatusScanResult} "Probe status and parsed device state"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node or command not found"
// @Router /api/v1/nodes/{id}/watchdog/status-scan/{commandId} [get]
func (h *NodeHandler) GetWatchdogStatusScan(c echo.Context) error {
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
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeStatusCheck, nodeID); aerr != nil {
		return aerr
	}
	res, err := h.nodeService.GetWatchdogStatusScanResult(c.Request().Context(), accountID, nodeID, commandID)
	if err != nil {
		return results.SendNotFoundError(c, "node or command not found")
	}
	return results.SendOKResponse(c, res, "watchdog status probe result")
}

// watchdogRecoveryLimited resolves the two install/node facts the flag needs —
// the effective boot-loop-failsafe threshold (per-node override, else the
// global config default) and the node's effective network_first policy — and
// hands them to the domain rule. A node that boots its OS directly, or an
// install with the failsafe off, has nothing behind a watchdog reboot loop,
// and the UI has to say so. Resolution failures degrade to "limited": when the
// platform cannot prove recovery exists, it must not claim it does.
func (h *NodeHandler) watchdogRecoveryLimited(c echo.Context, accountID, nodeID uuid.UUID, enabled bool) bool {
	if !enabled {
		return false
	}
	ctx := c.Request().Context()
	threshold := 0
	if h.config != nil {
		threshold = h.config.Provisioning.BootFailsafeThreshold
	}
	if n, err := h.nodeService.GetByIDAndAccount(ctx, nodeID, accountID); err == nil && n != nil {
		threshold = n.EffectiveBootFailsafeThreshold(threshold)
	}
	networkFirst := false
	if h.policyResolver != nil {
		if policy, err := h.policyResolver.ResolveEffectivePolicy(ctx, accountID, nodeID); err == nil {
			networkFirst = policy.NetworkFirst.Value
		}
	}
	return nodeDomain.WatchdogRecoveryLimited(enabled, threshold, networkFirst)
}

func (h *NodeHandler) mapWatchdogError(c echo.Context, err error) error {
	var facing results.OperatorFacingError
	switch {
	// A refusal that declared its own status is answered as itself — the
	// blanket 400 below is for domain validation failures, and it used to
	// swallow a watcher node's 409 and a device-busy conflict too.
	case errors.As(err, &facing):
		return results.SendInternalServerError(c, "watchdog operation failed", err)
	case errors.Is(err, echo.ErrNotFound):
		return results.SendNotFoundError(c, "node not found")
	default:
		// A validation failure from the domain is the common case here and is
		// worded for an operator, so it is passed through rather than masked.
		return results.SendValidationError(c, err.Error())
	}
}
