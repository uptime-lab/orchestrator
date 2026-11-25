package node

import (
	"errors"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Device-profile endpoints. The declaration is manual (a carrier board is not
// detectable), the apply is a dedicated queueable command, and a scan only
// CONFIRMS what was declared — see domain/device_profile.

// deviceProfileRequest is the PUT body: the node's own equipage. An empty
// profile_id clears the declaration (the node falls back to OU/account
// inheritance).
type deviceProfileRequest struct {
	ProfileID string `json:"profile_id"`
	VariantID string `json:"variant_id,omitempty"`
	Modules   []struct {
		ModuleID string `json:"module_id"`
		PortID   string `json:"port_id"`
	} `json:"modules,omitempty"`
}

// identifyRequest is the optional POST body for the locate blink.
type identifyRequest struct {
	DurationSec int `json:"duration_sec,omitempty"`
	// Stop ends a running blink instead of starting one.
	Stop bool `json:"stop,omitempty"`
}

// mapDeviceProfileError turns domain refusals into 400s with their message;
// anything unrecognized stays a 500.
func (h *NodeHandler) mapDeviceProfileError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, device_profile.ErrUnknownProfile),
		errors.Is(err, device_profile.ErrUnknownVariant),
		errors.Is(err, device_profile.ErrUnknownModule),
		errors.Is(err, device_profile.ErrUnknownPort),
		errors.Is(err, device_profile.ErrModuleDoesNotFit),
		errors.Is(err, device_profile.ErrModuleConflict),
		errors.Is(err, device_profile.ErrBusShared),
		errors.Is(err, device_profile.ErrProfileDoesNotFit):
		return results.SendValidationError(c, err.Error())
	}
	return results.SendInternalServerError(c, "device profile operation failed", err)
}

// mapDeviceProfileDispatchError maps dispatch outcomes: an offline node is an
// operator-actionable 400, not a server fault.
//
// A refusal that has DECLARED its own status is answered as itself (the
// WatcherNodeError/device-busy lesson: a blanket 400 here overrode their 409).
// Only errors MARKED operator-facing (app_errors.OperatorRefusal at the
// service's deliberate refusal sites) echo their text as a 400 — the blanket
// `SendValidationError(c, err.Error())` would echo raw repository and transport
// error text to the client (the same catch-all mapLEDError avoids).
func (h *NodeHandler) mapDeviceProfileDispatchError(c echo.Context, err error) error {
	var facing results.OperatorFacingError
	var refusal *app_errors.OperatorRefusalError
	switch {
	case errors.As(err, &facing):
		return results.SendInternalServerError(c, "device profile operation failed", err)
	case errors.Is(err, app_errors.ErrNodeNotFound):
		return results.SendNotFoundError(c, "node not found")
	case errors.As(err, &refusal), errors.Is(err, device_profile.ErrProfileDoesNotFit):
		return results.SendValidationError(c, err.Error())
	default:
		return results.SendInternalServerError(c, "device profile operation failed", err)
	}
}

// ListDeviceProfiles returns the hardware registry catalog.
// @Summary List device profiles and hardware modules
// @Description The registry of carrier-board profiles (fixed peripherals, factory variants, expansion ports) and attachable modules (bus needs, overlays, capabilities). Tenant-independent, served verbatim — the same catalog for every account (the workflow-blocks precedent), which is why the route carries no per-action authorization beyond authentication.
// @Tags device-profiles
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.DeviceProfileCatalogResult} "Catalog"
// @Router /api/v1/device-profiles [get]
func (h *NodeHandler) ListDeviceProfiles(c echo.Context) error {
	catalog := h.nodeService.DeviceProfileCatalog()
	if catalog == nil {
		return results.SendInternalServerError(c, "device profile registry is not available", nil)
	}
	return results.SendOKResponse(c, results.NewDeviceProfileCatalogResult(catalog), "device profiles retrieved")
}

// GetDeviceProfile returns the node's resolved equipage and its ladder.
// @Summary Get a node's device profile state
// @Description The effective equipage (resolved node -> OU -> root -> account, with the source level), per-option declared/applied/confirmed statuses, granted capabilities, the applied and desired render hashes, compatible profile ids for the picker, and the HAT identity the last scan reported.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeDeviceProfileResult} "Device profile state"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/device-profile [get]
func (h *NodeHandler) GetDeviceProfile(c echo.Context) error {
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
	state, err := h.nodeService.GetDeviceProfileState(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapDeviceProfileError(c, err)
	}
	return results.SendOKResponse(c, results.NewNodeDeviceProfileResult(state), "device profile state retrieved")
}

// PutDeviceProfile declares (or clears) the node's own equipage.
// @Summary Assign a node's device profile
// @Description Stores the node's own hardware declaration: carrier profile, factory variant, and which module sits in which port. Validated against the registry (unknown ids, module/port fit, bus and I2C-address conflicts, board compatibility) and REFUSED on any violation — a stored equipage must always render a safe config.txt. An empty profile_id clears the declaration; the node then follows OU/account inheritance. Assignment stores only: nothing reaches the device until an explicit apply.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param profile body deviceProfileRequest true "Equipage (empty profile_id clears)"
// @Success 200 {object} results.APIResponse{data=results.NodeDeviceProfileResult} "Resulting state"
// @Failure 400 {object} results.APIResponse "Validation refused"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/device-profile [put]
func (h *NodeHandler) PutDeviceProfile(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
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
	var req deviceProfileRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	eq := &device_profile.Equipage{ProfileID: req.ProfileID, VariantID: req.VariantID}
	for _, m := range req.Modules {
		eq.Modules = append(eq.Modules, device_profile.EquippedModule{ModuleID: m.ModuleID, PortID: m.PortID})
	}
	state, err := h.nodeService.SetDeviceProfile(c.Request().Context(), accountID, userID, nodeID, eq)
	if err != nil {
		return h.mapDeviceProfileError(c, err)
	}
	return results.SendOKResponse(c, results.NewNodeDeviceProfileResult(state), "device profile updated")
}

// GetDeviceProfilePreview renders what an apply would write.
// @Summary Preview a device profile apply
// @Description The exact config.txt managed-block content an apply would write, its hash, and the typed setup steps it would run — the confirm dialog's content, produced by the same render the apply uses. reboot_required is always true: config.txt is read at boot and the platform never reboots on its own.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.DeviceProfilePreviewResult} "Preview"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/device-profile/preview [get]
func (h *NodeHandler) GetDeviceProfilePreview(c echo.Context) error {
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
	preview, err := h.nodeService.PreviewDeviceProfile(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapDeviceProfileError(c, err)
	}
	return results.SendOKResponse(c, results.NewDeviceProfilePreviewResult(preview), "device profile preview rendered")
}

// ApplyDeviceProfile dispatches the apply command.
// @Summary Apply a node's device profile
// @Description Dispatches apply_device_profile: ONE queueable, managed-OS-only command that writes the device-profile managed block into config.txt and runs the equipage's typed setup steps, settling with one terminal status. Offline (or netbooted) nodes queue the apply for their next managed-OS connect — the response then carries queued=true and no command_id. Requires agent >= 0.6.0 (fail-closed; older agents answer 400). A reboot is required afterwards and is never performed automatically.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "command_id (when dispatched) and queued"
// @Failure 400 {object} results.APIResponse "No profile assigned / agent too old"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/device-profile/apply [post]
func (h *NodeHandler) ApplyDeviceProfile(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
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
	commandID, queued, err := h.nodeService.ApplyDeviceProfile(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return h.mapDeviceProfileDispatchError(c, err)
	}
	resp := map[string]interface{}{"queued": queued}
	if commandID != uuid.Nil {
		resp["command_id"] = commandID.String()
	}
	return results.SendOKResponse(c, resp, "device profile apply dispatched")
}

// ScanDeviceProfile dispatches the fixed hardware probe.
// @Summary Probe a node's hardware for the device profile ladder
// @Description Dispatches the fixed read-only probe (TPM/RTC device nodes, HAT ID EEPROM) and returns a command_id to poll. Refused while the node runs the netboot recovery image: the overlays were never in the boot config that booted it, so every probe would answer a false "absent". Detection only CONFIRMS the declaration — it never chooses a profile.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Probe dispatched; command_id to poll"
// @Failure 400 {object} results.APIResponse "Node offline or netbooted"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/device-profile/scan [post]
func (h *NodeHandler) ScanDeviceProfile(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeStatusCheck, nodeID); aerr != nil {
		return aerr
	}
	commandID, err := h.nodeService.ScanDeviceProfile(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return h.mapDeviceProfileDispatchError(c, err)
	}
	return results.SendOKResponse(c, map[string]string{"command_id": commandID.String()}, "device profile scan dispatched")
}

// GetDeviceProfileScan polls a dispatched scan.
// @Summary Poll a device profile scan
// @Description Reads a dispatched scan command; once the agent has reported, the parsed result (device paths present, HAT identity) is persisted as the node's scan state — which is what turns "applied" into "confirmed" or "not confirmed" in the ladder. The command must be the fixed scan this endpoint dispatches; arbitrary command ids are refused.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param commandId path string true "Command ID from the scan dispatch"
// @Success 200 {object} results.APIResponse{data=results.DeviceProfileScanResultDTO} "running | completed | failed"
// @Failure 404 {object} results.APIResponse "Node or command not found"
// @Router /api/v1/nodes/{id}/device-profile/scan/{commandId} [get]
func (h *NodeHandler) GetDeviceProfileScan(c echo.Context) error {
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
	status, err := h.nodeService.GetDeviceProfileScanResult(c.Request().Context(), accountID, nodeID, commandID)
	if err != nil {
		return results.SendNotFoundError(c, "scan not found")
	}
	return results.SendOKResponse(c, results.NewDeviceProfileScanResult(status), "device profile scan state")
}

// NodeIdentifyStates godoc
// @Summary Transient identify/button state for the account's nodes
// @Description Which nodes are blinking their locate LED and whose physical button was pressed recently (10-minute window) — the state a freshly opened page hydrates from. In-memory and transient by design.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.NodeIdentifyStateResult}
// @Failure 401 {object} results.APIResponse
// @Router /api/v1/nodes/identify-state [get]
func (h *NodeHandler) NodeIdentifyStates(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	// Same scoping the node LIST applies: an OU-scoped viewer sees only the
	// entries of nodes it may read.
	allowAll, allowedOUs, err := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, authz.ActionNodeRead)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	states := h.nodeService.IdentifyStates(accountID)
	if !allowAll {
		filtered := states[:0]
		for _, st := range states {
			n, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), st.NodeID, accountID)
			if err != nil || n.OUID == nil || !allowedOUs[*n.OUID] {
				continue
			}
			filtered = append(filtered, st)
		}
		states = filtered
	}
	return results.SendOKResponse(c, results.NewNodeIdentifyStateResults(states), "identify state")
}

// IdentifyNode blinks the node's locate LED.
// @Summary Identify (locate) a node
// @Description Blinks the node's locate LED so a human can find the physical machine: the universal backend blinks the sysfs ACT LED, boards whose profile declares digital LEDs get their configured rendering. The agent reports success immediately after starting the blink and stops on its own timer (the account's identify duration setting, default 300 s, max 3600 s — the response carries the effective duration_sec); a repeat call restarts it. Not queueable — an offline node fails fast, because a locate blink hours after the operator left the rack is a surprise. Requires agent >= 0.6.0.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param body body identifyRequest false "Optional blink duration, or stop=true to end a running blink"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "command_id"
// @Failure 400 {object} results.APIResponse "Node offline / agent too old"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent, or another command owns the device"
// @Router /api/v1/nodes/{id}/identify [post]
func (h *NodeHandler) IdentifyNode(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeStatusCheck, nodeID); aerr != nil {
		return aerr
	}
	var req identifyRequest
	_ = c.Bind(&req) // body is optional
	commandID, durationSec, err := h.nodeService.Identify(c.Request().Context(), accountID, userID, nodeID, req.DurationSec, req.Stop)
	if err != nil {
		return h.mapDeviceProfileDispatchError(c, err)
	}
	// duration_sec is the EFFECTIVE duration (the account setting when the
	// caller named none) — the web lamp blinks exactly as long as the device.
	return results.SendOKResponse(c, map[string]interface{}{
		"command_id":   commandID.String(),
		"duration_sec": durationSec,
	}, "identify dispatched")
}
