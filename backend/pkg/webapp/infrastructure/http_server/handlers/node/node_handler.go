package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_file_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/eeprom_provision"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	nodeCommandDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	node_fileDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	organizationalUnit "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/provisioning"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"golang.org/x/time/rate"
)

// NodeHandler handles node-related HTTP requests
// EffectivePolicyResolver resolves a node's effective inheritable policy
// (node -> OU -> root). Satisfied by the organizational_unit service.
type EffectivePolicyResolver interface {
	ResolveEffectivePolicy(ctx context.Context, accountID, nodeID uuid.UUID) (organizationalUnit.EffectivePolicy, error)
}

type NodeHandler struct {
	nodeService        services.NodeService
	nodeFileService    services.FileService
	nodeCommandService services.NodeCommandService
	policyResolver     EffectivePolicyResolver
	enforcer           *authz_service.Enforcer
	audit              *audit.Recorder
	signer             *eepromsigner.Client
	eepromProvision    *eeprom_provision.Service
	logger             *slog.Logger
	config             *config.Config

	// Cached signer /base response: the firmware base only changes when the
	// host's rpi-eeprom clone is updated (daily job), so a short TTL is plenty.
	// baseFW is shared with the fleet attention check; see eepromsigner.BaseCache.
	baseFW *eepromsigner.BaseCache

	// runCommands applies the run view's omission to command reads; see
	// node_command_run_visibility.go.
	runCommands runCommandFilter
}

// NewNodeHandler creates a new node handler. policyResolver and signer may be nil,
// which disables network-first EEPROM provisioning (auto-on-approve and explicit).
func NewNodeHandler(nodeService services.NodeService, nodeFileService services.FileService, nodeCommandService services.NodeCommandService, policyResolver EffectivePolicyResolver, enforcer *authz_service.Enforcer, auditRecorder *audit.Recorder, signer *eepromsigner.Client, baseFW *eepromsigner.BaseCache, logger *slog.Logger, cfg *config.Config) *NodeHandler {
	// Required: the command endpoints here serve resolved secret material to a
	// caller holding secret.read_value, and that read has to be recorded.
	if auditRecorder == nil {
		panic("NodeHandler requires an audit recorder")
	}
	return &NodeHandler{
		nodeService:        nodeService,
		nodeFileService:    nodeFileService,
		nodeCommandService: nodeCommandService,
		policyResolver:     policyResolver,
		enforcer:           enforcer,
		audit:              auditRecorder,
		signer:             signer,
		baseFW:             baseFW,
		// Build the shared provisioning service from the handler's own deps; the
		// gRPC first-boot hook reuses this same instance via EEPROMProvisionService().
		eepromProvision: eeprom_provision.NewService(signer, policyResolver, nodeService, cdnsign.DeriveKey(cfg.CDNSigningSecret()), logger),
		logger:          logger.With("handler", "node"),
		config:          cfg,
	}
}

// EEPROMProvisionService exposes the shared network-first provisioning service so
// the composition root can wire the same instance into the gRPC server's
// first-system-info auto-provision hook.
func (h *NodeHandler) EEPROMProvisionService() *eeprom_provision.Service {
	return h.eepromProvision
}

// authorizeNodeAction checks the action against the node's OU scope. It loads
// the node (also yielding a clean 404 for a wrong/foreign id) and returns a
// non-nil error response when denied — handlers do `if err := ...; err != nil
// { return err }`.
func (h *NodeHandler) authorizeNodeAction(c echo.Context, action authz.Action, nodeID uuid.UUID) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	node, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), nodeID, accountID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	// Returns nil (allowed) or a non-nil *echo.HTTPError (denied); the caller
	// MUST propagate it. See middleware.Authorize.
	return middleware.Authorize(c, h.enforcer, action, node.OUID)
}

// RegisterRoutes registers node routes
func (h *NodeHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	nodes := e.Group("/api/v1/nodes")
	nodes.Use(authMiddleware)

	// Manual node creation is admin+ (account-scoped: no node/OU exists yet).
	nodes.POST("", h.Create, middleware.RequireAction(h.enforcer, authz.ActionNodeCreate))
	// Deleted-node records, registered BEFORE the `:id` routes below (see
	// deleted_nodes.go).
	h.registerDeletedNodeRoutes(nodes)
	h.registerAdoptionRefusalRoutes(nodes)
	nodes.GET("/:id", h.Get)
	nodes.GET("", h.List)
	nodes.PUT("/:id", h.Update)
	nodes.DELETE("/:id", h.Delete)
	nodes.GET("/:id/adoption-file", h.GetAdoptionFile)
	nodes.POST("/:id/adoption-code", h.CreateAdoptionCode)
	nodes.POST("/:id/provisioning/sd", h.DownloadNodeSDImage)
	nodes.POST("/:id/command", h.SendCommand)
	nodes.POST("/commands", h.SendBatchCommand)
	nodes.GET("/:id/node-commands", h.GetNodeCommands)

	// Tracked-file routes. The path travels as a query/body field, never as a
	// route segment: the old `/files/*` + `/files/*/versions` pair could not
	// coexist — Echo let the later registration win, so GetFile was dead code
	// and the editor always received a version list it read as empty content.
	nodes.GET("/:id/files", h.GetFiles)
	nodes.GET("/:id/files/content", h.GetFile)
	nodes.GET("/:id/files/versions", h.GetFileVersions)
	nodes.PUT("/:id/files/content", h.UpdateFile)
	nodes.POST("/:id/files/:file_id/apply", h.ApplyFile)
	nodes.POST("/:id/files/track", h.TrackFile)
	nodes.POST("/:id/files/refresh", h.RefreshFile)
	nodes.GET("/:id/files/read/:commandId", h.GetFileReadResult)
	nodes.POST("/:id/files/untrack", h.UntrackFile)
	nodes.POST("/:id/files/delete-on-device", h.DeleteFileOnDevice)

	// Watchdog supervision. The preset catalog is the source of truth for the
	// picker and its confirmation wording; status-scan probes the DEVICE for
	// whether the daemon is actually running, which stored state cannot answer.
	nodes.GET("/:id/watchdog", h.GetWatchdog)
	nodes.PUT("/:id/watchdog", h.PutWatchdog)
	nodes.DELETE("/:id/watchdog", h.DeleteWatchdog)
	nodes.GET("/:id/watchdog-presets", h.GetWatchdogPresets)

	// LED behaviour: stealth mode / ACT+PWR disable / ambient indication.
	// Validation against the board's declared wiring happens in the service.
	nodes.GET("/:id/led-config", h.GetLED)
	nodes.PUT("/:id/led-config", h.PutLED)
	nodes.DELETE("/:id/led-config", h.DeleteLED)
	nodes.POST("/:id/watchdog/status-scan", h.ScanWatchdogStatus)
	nodes.GET("/:id/watchdog/status-scan/:commandId", h.GetWatchdogStatusScan)

	// Node approval routes (for master token auth)
	nodes.POST("/:id/approve", h.Approve)
	nodes.POST("/:id/reject", h.Reject)
	nodes.POST("/:id/provision-eeprom", h.ProvisionEEPROM)

	// Bootloader firmware base the signer currently bakes from (per SoC).
	eeprom := e.Group("/api/v1/eeprom")
	eeprom.Use(authMiddleware)
	eeprom.GET("/base-firmware", h.GetBaseFirmware)
	// WebUSB flasher: generic second-stage bootcode (serial harvest) + bake a
	// per-serial signed EEPROM tied to a created/matched node.
	eeprom.GET("/bootcode/:soc", h.GetBootcode)
	eeprom.POST("/webusb-provision", h.WebUSBProvision, middleware.RequireAction(h.enforcer, authz.ActionNodeCreate))
	eeprom.POST("/webusb-provision/:id/committed", h.WebUSBProvisionCommit, middleware.RequireAction(h.enforcer, authz.ActionNodeCreate))
	nodes.POST("/:id/reboot-to-recovery", h.RebootToRecovery)
	nodes.POST("/:id/clear-boot-failsafe", h.ClearBootFailsafe)

	// OS-user inventory: dispatch a fixed read-only scan (node.credentials_read)
	// then poll its parsed result. Two steps because the command path is
	// async — dispatch returns an id, a gRPC status update completes it.
	nodes.POST("/:id/os-users/scan", h.ScanOSUsers)
	nodes.GET("/:id/os-users/scan/:commandId", h.GetOSUserScan)

	// Live EEPROM config dump (the actually-flashed rpi-eeprom-config), dispatch
	// + poll like the OS-user scan. Gated on EEPROM-flash rights: it exposes the
	// boot path (HTTP_HOST/PATH, boot order, embedded-key presence).
	nodes.POST("/:id/eeprom-config/scan", h.ScanEEPROMConfig)
	nodes.GET("/:id/eeprom-config/scan/:commandId", h.GetEEPROMConfigScan)

	// Overclock presets: read the catalog for this node's board / apply one
	// (dispatches patch_file into a managed config.txt block; reboot required).
	nodes.GET("/:id/overclock-presets", h.GetOverclockPresets)
	nodes.POST("/:id/overclock", h.ApplyOverclockPreset)

	// Device profiles: declared hardware equipage (carrier board, factory
	// variant, plugged modules), preview/apply of its config.txt block, the
	// confirming hardware scan, and the locate blink. The catalog group is
	// authed but action-free — Registry content, identical for every tenant
	// (the workflow-blocks precedent).
	profiles := e.Group("/api/v1/device-profiles")
	profiles.Use(authMiddleware)
	profiles.GET("", h.ListDeviceProfiles)
	nodes.GET("/:id/device-profile", h.GetDeviceProfile)
	nodes.PUT("/:id/device-profile", h.PutDeviceProfile)
	nodes.GET("/:id/device-profile/preview", h.GetDeviceProfilePreview)
	nodes.POST("/:id/device-profile/apply", h.ApplyDeviceProfile)
	nodes.POST("/:id/device-profile/scan", h.ScanDeviceProfile)
	nodes.GET("/:id/device-profile/scan/:commandId", h.GetDeviceProfileScan)
	nodes.POST("/:id/identify", h.IdentifyNode)
	// Static segment beats :id in Echo, so this coexists with the per-node
	// routes. The transient identify/button registry a fresh page hydrates from.
	nodes.GET("/identify-state", h.NodeIdentifyStates)

	// Public connection endpoint (no auth required). Rate-limited like the
	// other public endpoints: the 122-bit unique_token is not guessable, but
	// without a limiter this was free unauthenticated load — a DB lookup and a
	// constant-time compare per attempt, plus a write on first connect.
	// Same envelope as /auth and /adopt/exchange.
	connectLimit := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	})
	e.POST("/api/v1/nodes/connect", h.Connect, connectLimit)
}

// Create creates a new node
// @Summary Create node
// @Description Creates a new node. Both provisioning credentials are optional: default_username names the console account provisioning creates, and default_password (which needs default_username) only sets a login for the physical console — remote access is expected to come from an SSH key or an access entry, and provision_connection refuses a node that resolves neither.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param node body node_create.NodeCreateCommand true "Node creation data"
// @Success 201 {object} results.APIResponse{data=results.NodeResult} "Node created successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "The account is at its node cap, the install at its device cap (code device_cap_reached), or the account at its own device limit (code account_device_limit_reached; data: limit, account_devices, plan, until for a dated subscription, contact)"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 503 {object} results.APIResponse "The device cap could not be checked (code device_cap_unverified): nothing was written; try again"
// @Router /api/v1/nodes [post]
func (h *NodeHandler) Create(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd node_create.NodeCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Both provisioning credentials are optional: together they describe a local
	// console login, and access to a managed node comes from access entries.
	// A password with no username is the one incoherent combination.
	if cmd.DefaultUsername == "" && cmd.DefaultPassword != "" {
		return results.SendValidationError(c, "default_username is required when a default_password is set")
	}

	// Set auth context and generate ID
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	node, err := h.nodeService.Create(c.Request().Context(), cmd)
	if err != nil {
		// A quota refusal is 409, not 500: the request was well-formed and the
		// caller has an action available (remove a node). A 500 would tell them
		// the platform is broken and to try again, which is the wrong advice.
		if errors.Is(err, app_errors.ErrNodeLimitReached) {
			h.logger.WarnContext(c.Request().Context(), "refusing node create: account is at its node cap",
				"account_id", accountID)
			return results.SendError(c, http.StatusConflict, err.Error(), err)
		}
		results.LogFailure(c.Request().Context(), h.logger, "failed to create node", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to create node", err)
	}

	nodeResult := results.NewNodeResult(node)
	return results.SendCreatedResponse(c, nodeResult, "node created successfully")
}

// Get retrieves a node by ID
// @Summary Get node by ID
// @Description Retrieves node details by ID
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id} [get]
func (h *NodeHandler) Get(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// OU-scoped read: a node in an OU the caller cannot read must not be visible,
	// mirroring the mutating paths (single reads were account-scoped but not
	// OU-scoped). Denied -> 403/404, so it is hidden.
	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr
	}

	query := node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	node, err := h.nodeService.GetByID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to get node", "error", err, "node_id", nodeID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	nodeResult := results.NewNodeResult(node)
	return results.SendOKResponse(c, nodeResult, "node retrieved successfully")
}

// List retrieves all nodes for the account
// @Summary List nodes
// @Description Retrieves the account's nodes. Optional ou_id filter: a unit UUID for direct members, or "unassigned" for nodes in no unit. Optional search/sort, and page/per_page pagination: when either paging param is present the response switches to the paginated envelope (results.NodePageResult: nodes/total/page/per_page); without them the full legacy list is returned.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param ou_id query string false "Filter by OU membership (UUID or \"unassigned\")"
// @Param search query string false "Case-insensitive substring match on name/hostname/IPv4/MAC/serial/status"
// @Param sort query string false "Sort column (name|status|last_seen_at|created_at, default name)"
// @Param order query string false "Sort order (asc|desc, default asc)"
// @Param page query integer false "1-based page; presence of page or per_page selects the paginated envelope"
// @Param per_page query integer false "Page size (default 50, max 200)"
// @Success 200 {object} results.APIResponse{data=results.NodeListResult} "Nodes retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes [get]
func (h *NodeHandler) List(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// OU-scoped users see only nodes in their allowed OU subtree; account-wide
	// readers (and super_admin) see everything. OU-less nodes are visible to
	// account-wide readers only. Resolved BEFORE the list is loaded: a removed
	// member is refused here, without the tenant's node list having been read
	// on their behalf first.
	allowAll, allowedOUs, err := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, authz.ActionNodeRead)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}

	query := node_list.NodeListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	nodes, err := h.nodeService.GetAllByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list nodes", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list nodes", err)
	}

	// Optional membership filter (Organization page deep link): a unit id for
	// its direct members, or "unassigned". Applied on top of the authz filter,
	// never instead of it.
	ouParam := c.QueryParam("ou_id")
	var ouFilter *uuid.UUID
	filterUnassigned := false
	switch ouParam {
	case "":
	case "unassigned":
		filterUnassigned = true
	default:
		ouID, err := uuid.Parse(ouParam)
		if err != nil {
			return results.SendValidationError(c, "invalid ou_id: must be a UUID or \"unassigned\"")
		}
		ouFilter = &ouID
	}

	params := nodeListParams{
		Search:   c.QueryParam("search"),
		Sort:     c.QueryParam("sort"),
		Order:    c.QueryParam("order"),
		paginate: c.QueryParam("page") != "" || c.QueryParam("per_page") != "",
	}
	if raw := c.QueryParam("page"); raw != "" {
		params.Page, _ = strconv.Atoi(raw)
	}
	if raw := c.QueryParam("per_page"); raw != "" {
		params.PerPage, _ = strconv.Atoi(raw)
	}
	if err := params.validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	nodeResults := make([]results.NodeResult, 0, len(nodes))
	for i := range nodes {
		node := nodes[i]
		if !allowAll {
			if node.OUID == nil || !allowedOUs[*node.OUID] {
				continue
			}
		}
		if filterUnassigned && node.OUID != nil {
			continue
		}
		if ouFilter != nil && (node.OUID == nil || *node.OUID != *ouFilter) {
			continue
		}
		nodeResult := results.NewNodeResult(&node)
		if !matchesSearch(&nodeResult, params.Search) {
			continue
		}
		nodeResults = append(nodeResults, nodeResult)
	}
	sortNodeResults(nodeResults, params.Sort, params.Order)

	if params.paginate {
		page := results.NewNodePageResult(
			pageSlice(nodeResults, params.Page, params.PerPage),
			len(nodeResults), params.Page, params.PerPage)
		return results.SendOKResponse(c, page, "nodes retrieved successfully")
	}
	listResult := results.NewNodeListResult(nodeResults)
	return results.SendOKResponse(c, listResult, "nodes retrieved successfully")
}

// Update updates an existing node
// @Summary Update node
// @Description Updates node information
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param node body node_update.NodeUpdateCommand true "Node update data (*string fields: omitted means unchanged)"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id} [put]
func (h *NodeHandler) Update(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// Editing node settings (name, ssh key, default credentials) is admin+.
	if err := h.authorizeNodeAction(c, authz.ActionNodeUpdate, nodeID); err != nil {
		return err
	}

	var cmd node_update.NodeUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and node ID
	cmd.ID = nodeID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	node, err := h.nodeService.Update(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update node", err, "node_id", nodeID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		if errors.Is(err, nodeDomain.ErrNotesTooLong) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to update node", err)
	}

	nodeResult := results.NewNodeResult(node)
	return results.SendOKResponse(c, nodeResult, "node updated successfully")
}

// Delete deletes a node
// @Summary Delete node
// @Description Deletes a node by ID. A node whose device is ONLINE is refused with 409 unless force=true: deleting it there strands the device, which keeps retrying with an identity the platform no longer knows. A forced delete also asks the agent to release its own adoption file (agent >= 0.5.0) so the device returns to a clean unadopted state. Either way the deletion is recorded under GET /api/v1/nodes/deleted, from which the node can be restored.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param force query boolean false "Delete even though the device is online, and ask its agent to release its adoption identity"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The device is online and force was not set"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id} [delete]
func (h *NodeHandler) Delete(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.authorizeNodeAction(c, authz.ActionNodeDelete, nodeID); err != nil {
		return err
	}

	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// force=true is required to delete a node whose device is ONLINE, and it also
	// asks the agent to release its own adoption identity on the way out. Without
	// it the delete is refused: this is the last moment the platform can reach the
	// device, and deleting here is exactly what strands it.
	force := strings.EqualFold(c.QueryParam("force"), "true")

	err = h.nodeService.Delete(c.Request().Context(), accountID, actorUserID, nodeID, force)
	if err != nil {
		// The force refusal is a POLICY DECISION, not a failure, so it is logged
		// before (and instead of) the error line below and at Info. An operator
		// meets this gate on purpose; logging it at ERROR would put a red line in
		// the log every time the guard does its job, which is how people learn to
		// scroll past ERROR.
		if errors.Is(err, app_errors.ErrNodeOnlineDeleteRequiresForce) {
			h.logger.InfoContext(c.Request().Context(), "refused to delete a node whose device is online; force not set",
				"node_id", nodeID)
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
		h.logger.Error("failed to delete node", "error", err, "node_id", nodeID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to delete node", err)
	}

	return results.SendNoContentResponse(c)
}

// GetAdoptionFile downloads the adoption file for a node
// @Summary Get node adoption file
// @Description Downloads the adoption file for a node
// @Tags nodes
// @Accept json
// @Produce text/plain
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {string} string "Adoption file content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/adoption-file [get]
func (h *NodeHandler) GetAdoptionFile(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// The adoption file contains the node's unique device credential — a
	// credential-bearing read, not part of viewer/manager read access.
	if err := h.authorizeNodeAction(c, authz.ActionNodeCredentials, nodeID); err != nil {
		return err
	}

	// Get platform URL - use advertisement URL if configured, otherwise use request host
	var platformURL string
	if h.config.Server.AdvertisementURL != "" {
		platformURL = h.config.Server.AdvertisementURL
	} else {
		// c.Scheme() honors X-Forwarded-Proto — behind the TLS-terminating
		// proxy Request().TLS is nil and would mislabel the origin as http.
		platformURL = c.Scheme() + "://" + c.Request().Host
	}

	// Construct gRPC endpoint - use advertisement URL if configured, otherwise use host:port
	var grpcEndpoint string
	if h.config.GRPC.AdvertisementURL != "" {
		grpcEndpoint = h.config.GRPC.AdvertisementURL
	} else {
		grpcEndpoint = net.JoinHostPort(h.config.GRPC.Host, fmt.Sprintf("%d", h.config.GRPC.Port))
	}

	// Get adoption file content
	adoptionContent, err := h.nodeService.GetAdoptionInfo(c.Request().Context(), accountID, nodeID, platformURL, grpcEndpoint)
	if err != nil {
		h.logger.Error("failed to get adoption info", "error", err, "node_id", nodeID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get adoption info", err)
	}

	// Set response headers for file download
	c.Response().Header().Set("Content-Type", "text/plain")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=node-adoption-"+nodeID.String()+".txt")

	return c.String(200, adoptionContent)
}

// CreateAdoptionCode mints the one-time exchange code for the node's
// one-command install link
// @Summary Create a one-time adoption code
// @Description Mints a short-lived single-use code for the node's one-command install link; the device exchanges it over TLS for the adoption config. Re-minting revokes the previous code. The link no longer carries the adoption blob (account id, topology, reusable credential).
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "code, expires_at, install_command"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/adoption-code [post]
func (h *NodeHandler) CreateAdoptionCode(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	// Same gate as the adoption file: the code exchanges into the node's
	// device credential.
	if err := h.authorizeNodeAction(c, authz.ActionNodeCredentials, nodeID); err != nil {
		return err
	}

	code, expiresAt, err := h.nodeService.CreateAdoptionCode(c.Request().Context(), accountID, nodeID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		h.logger.Error("failed to create adoption code", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to create adoption code", err)
	}

	var platformURL string
	if h.config.Server.AdvertisementURL != "" {
		platformURL = h.config.Server.AdvertisementURL
	} else {
		platformURL = c.Scheme() + "://" + c.Request().Host
	}
	installCommand := adoptionInstallCommand(platformURL, code)

	return results.SendOKResponse(c, map[string]string{
		"code":            code,
		"expires_at":      expiresAt.Format(time.RFC3339),
		"install_command": installCommand,
	}, "adoption code created")
}

// DownloadNodeSDImage streams a per-node SD/USB provisioning bundle.
// @Summary Download a per-node SD/USB provisioning bundle
// @Description Streams a ZIP with the boot-partition files plus a node-adoption.txt bound to THIS node's per-node token (not a master token). Unzip onto a FAT32 SD card or USB drive; the device boots the bootstrap image, the agent reads the adoption file, binds its hardware to this pre-created node and comes up already approved. If the node was created with network-first provisioning, the EEPROM is flashed on the first system-info report.
// @Tags nodes
// @Accept json
// @Produce application/zip
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {file} binary "ZIP provisioning bundle"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/provisioning/sd [post]
func (h *NodeHandler) DownloadNodeSDImage(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.authorizeNodeAction(c, authz.ActionNodeProvisioningSD, nodeID); err != nil {
		return err
	}

	// Validate the payload directory up front, before we send any headers, so a
	// misconfigured server returns a clean error instead of a truncated stream.
	payloadDir := h.config.Provisioning.SDPayloadDir
	if payloadDir == "" {
		return results.SendInternalServerError(c, "SD provisioning is not configured on this server", fmt.Errorf("empty PROVISIONING_SD_PAYLOAD_DIR"))
	}
	files, err := provisioning.CollectPayloadFiles(payloadDir)
	if err != nil {
		h.logger.Error("failed to read SD provisioning payload directory", "error", err, "dir", payloadDir)
		return results.SendInternalServerError(c, "SD provisioning payload is unavailable", err)
	}
	if len(files) == 0 {
		return results.SendInternalServerError(c, "SD provisioning payload is empty", fmt.Errorf("no files in %s", payloadDir))
	}

	// Build the adoption file bound to THIS node's per-node token. GetAdoptionInfo
	// also yields a clean 404 for a wrong/foreign id.
	adoptionFile, err := h.nodeService.GetAdoptionInfo(c.Request().Context(), accountID, nodeID, h.platformURL(c), h.grpcEndpoint())
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		h.logger.Error("failed to build adoption file", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to build adoption file", err)
	}

	c.Response().Header().Set(echo.HeaderContentType, "application/zip")
	c.Response().Header().Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="orchestrator-node-%s.zip"`, nodeID.String()))
	c.Response().WriteHeader(200)

	if err := provisioning.WriteSDBundle(c.Response(), payloadDir, files, adoptionFile); err != nil {
		h.logger.Error("failed to stream per-node provisioning bundle", "error", err, "node_id", nodeID)
		return err
	}
	h.logger.Info("per-node SD provisioning bundle generated", "node_id", nodeID, "account_id", accountID, "files", len(files))
	return nil
}

// nodeConnectRequest is the agent's connect body — a named type so the
// document can carry the real schema (it used to publish a bare object).
type nodeConnectRequest struct {
	NodeID      uuid.UUID `json:"node_id"`
	AccountID   uuid.UUID `json:"account_id"`
	UniqueToken string    `json:"unique_token"`
	Fingerprint struct {
		MacAddress     string `json:"mac_address"`
		HardwareSerial string `json:"hardware_serial"`
	} `json:"fingerprint"`
}

// Connect handles node connection from agent
// @Summary Connect node
// @Description Connects a node from an agent using node credentials and hardware fingerprint
// @Tags nodes
// @Accept json
// @Produce json
// @Param connection body nodeConnectRequest true "Node connection data"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node connected successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 404 {object} results.APIResponse "Node not found or invalid credentials"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/connect [post]
func (h *NodeHandler) Connect(c echo.Context) error {
	var req nodeConnectRequest

	if err := c.Bind(&req); err != nil {
		h.logger.Error("failed to bind connection request", "error", err)
		return results.SendValidationError(c, "invalid request format")
	}

	// Validate required fields
	if req.NodeID == uuid.Nil || req.AccountID == uuid.Nil || req.UniqueToken == "" {
		h.logger.Error("missing required fields in connection request")
		return results.SendValidationError(c, "node_id, account_id, and unique_token are required")
	}

	// Create hardware fingerprint
	fingerprint := nodeDomain.HardwareFingerprint{
		MacAddress:     req.Fingerprint.MacAddress,
		HardwareSerial: req.Fingerprint.HardwareSerial,
	}

	// Create connection command
	cmd := node_connect.NodeConnectCommand{
		NodeID:      req.NodeID,
		AccountID:   req.AccountID,
		UniqueToken: req.UniqueToken,
		Fingerprint: fingerprint,
	}

	// Connect the node
	connectedNode, err := h.nodeService.Connect(c.Request().Context(), cmd)
	if err != nil {
		h.logger.Error("failed to connect node", "error", err, "node_id", req.NodeID, "account_id", req.AccountID)
		// Serial conflict answers byte-identically to a bad token: this endpoint
		// is unauthenticated and must stay a zero-information oracle.
		if errors.Is(err, app_errors.ErrNodeNotFound) || errors.Is(err, app_errors.ErrHardwareSerialConflict) {
			return results.SendNotFoundError(c, "node not found or invalid credentials")
		}
		// nil err: transient error details must not reach an unauthenticated caller.
		return results.SendInternalServerError(c, "failed to connect node", nil)
	}

	nodeResult := results.NewNodeResult(connectedNode)
	return results.SendOKResponse(c, nodeResult, "node connected successfully")
}

// SendCommand sends a command to a node
// @Summary Send command to node
// @Description Sends a command (reboot, shutdown, status_check) to a node
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param command body node_command.NodeCommandCommand true "Command data (slug + per-command params)"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Command accepted. data.command_id names it; data.delivery is dispatched (handed to the connected node) or queued (a queueable command registered for an offline node, delivered on reconnect). Either way the outcome arrives later on the command itself."
// @Failure 400 {object} results.APIResponse "Validation error: a parameter of the wrong shape, a required parameter missing or blank, a value the command refuses (a relative or refused file path, an unknown manage_service action, a download checksum that is not SHA-256, a credential given to provision_connection, which resolves it from the provisioning policy), an install_os image that cannot be installed (not found, still being processed, no file), or an agent too old for the command"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 503 {object} map[string]string "Node not connected"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 409 {object} map[string]string "The node's state refuses the command: offline or not approved (node_unavailable, node_not_approved), already on the requested agent version (agent_already_current; set reinstall to true to reinstall), a watcher (Watcher) node, a disk already owned by another command, a Watcher install the device could not return from, a recovery-only command (capture_image, patch_file_offline, apply_access_config_offline) for a node running its installed OS (recovery_required), an erase of the disk the node runs from (boot_drive), a capture the account's image cap or quota has no room for (image_limit), or a provision_connection whose node resolves no OS username or no password or SSH key for it (provisioning_incomplete)"
// @Router /api/v1/nodes/{id}/command [post]
func (h *NodeHandler) SendCommand(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse node ID from URL parameter
	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// Parse request body directly into command
	var cmd node_command.NodeCommandCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request format")
	}

	// Validate command is provided
	if cmd.Command == "" {
		return results.SendValidationError(c, "command is required")
	}

	// The authz action is derived from the command slug (fail closed on an
	// unmapped slug) and checked against the node's OU scope.
	action, ok := authz.ActionForCommand(cmd.Command)
	if !ok {
		return results.SendValidationError(c, "unknown command")
	}
	if err := h.authorizeNodeAction(c, action, nodeID); err != nil {
		return err
	}

	// Set fields from URL and context
	cmd.NodeID = nodeID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Dispatched as a one-node batch rather than through SendCommand, because
	// the answer needs what the batch item carries: the command's id, and
	// whether it was handed to the node or only queued for it.
	item, err := h.dispatchOne(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to send command", commandFailureForLog(err),
			"node_id", nodeID, "account_id", accountID, "command", cmd.Command)

		// Check error type and return appropriate HTTP status
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}

		// An oversized bulk field is a client error, refused before the command is
		// ever dispatched — 400 rather than a dropped stream.
		var tooLarge *node_service.CommandPayloadTooLargeError
		if errors.As(err, &tooLarge) {
			return results.SendValidationError(c, tooLarge.Error())
		}

		// The node's agent predates the command (or, for install_agent
		// variant=watcher, the variant field). Without this arm the typed
		// error flattens into the generic 500 below, which tells the operator
		// the platform broke rather than that the recovery agent needs
		// updating — exactly the "failed to send command" an operator sees
		// trying to install Watcher onto a recovery agent that predates it.
		var tooOld *node_service.AgentTooOldError
		if errors.As(err, &tooOld) {
			return results.SendValidationError(c, tooOld.Error())
		}

		// A hostname the device would reject is refused before dispatch.
		// Without this arm it flattens into the generic 500 below, which
		// tells the operator the platform broke rather than that their value did.
		if errors.Is(err, nodeCommandDomain.ErrInvalidHostname) {
			return results.SendValidationError(c, err.Error())
		}

		// The four bare `{"error": <code>, "message": …}` bodies below are
		// DELIBERATE and stay outside the results envelope: this route's clients
		// parse that legacy shape, so its 409/503 refusals are the one place the
		// API still answers bare (the CDN handler is the other, for a different
		// reason). Every other route answers through results.Send*.
		//
		// A second command for a disk that is already being written.
		// 409, not 429 or 400: the caller's next step is to wait for the
		// command that is already running, whose id the message carries — not to
		// fix their request. Same reason the per-tenant caps answer 409.
		var busy *node_service.DeviceBusyError
		if errors.As(err, &busy) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "conflict",
				"message": busy.Error(),
			})
		}

		// A watcher node executes no commands. Without this arm
		// the typed refusal flattens into the generic 500 below, which tells
		// the operator the platform broke rather than that this node is
		// watcher-only and how to manage it instead. 409: the request is
		// well-formed, the node's state refuses it.
		var watcher *node_service.WatcherNodeError
		if errors.As(err, &watcher) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "watcher_node",
				"message": watcher.Error(),
			})
		}

		// A Watcher install refused because the device could not be managed
		// again afterwards. 409 for the DeviceBusyError reason: the request is
		// well-formed and the caller's next step is to fix the device's boot
		// order, which the message names — not to retry.
		var unsafeWatcherInstall *node_service.WatcherInstallUnsafeError
		if errors.As(err, &unsafeWatcherInstall) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "watcher_install_unsafe",
				"message": unsafeWatcherInstall.Error(),
			})
		}

		// The node is offline (or not approved) and the command cannot wait
		// for it. 409: the request is fine, the node's state refuses it. It
		// flattened into the generic 500 before, which reads as the platform
		// breaking when the node was simply away.
		var unavailable *node_service.NodeUnavailableError
		if errors.As(err, &unavailable) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   unavailable.ErrorCode(),
				"message": unavailable.Error(),
			})
		}

		// A recovery-only command (capture_image, the offline patch and
		// access apply) for a node running its installed OS, and an erase
		// aimed at the disk the node runs from. 409: the request is
		// well-formed, the node's state refuses it, and the message names
		// what to do instead.
		var recoveryRequired *node_service.RecoveryRequiredError
		if errors.As(err, &recoveryRequired) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   recoveryRequired.ErrorCode(),
				"message": recoveryRequired.Error(),
			})
		}
		var bootDrive *node_service.BootDriveEraseError
		if errors.As(err, &bootDrive) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   bootDrive.ErrorCode(),
				"message": bootDrive.Error(),
			})
		}

		// A capture the account's image cap or stored-image quota has no room
		// for: the next step is to delete an image.
		var imageLimit *node_service.CaptureImageLimitError
		if errors.As(err, &imageLimit) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   imageLimit.ErrorCode(),
				"message": imageLimit.Error(),
			})
		}

		// update_agent naming the version the node already runs.
		var current *node_service.AgentAlreadyCurrentError
		if errors.As(err, &current) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   current.ErrorCode(),
				"message": current.Error(),
			})
		}

		// provision_connection for a node whose provisioning policy names no
		// account it could create: the message names what to set, and where.
		var incomplete *node_service.ProvisioningIncompleteError
		if errors.As(err, &incomplete) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   incomplete.ErrorCode(),
				"message": incomplete.Error(),
			})
		}

		errStr := err.Error()

		// Check if error indicates node is not connected
		var notConnected *node_service.NodeNotConnectedError
		if errors.As(err, &notConnected) ||
			errStr == fmt.Sprintf("node %s is not connected", nodeIDStr) ||
			errStr == fmt.Sprintf("node %s not connected", nodeIDStr) {
			return c.JSON(503, map[string]string{
				"error":   "service_unavailable",
				"message": "node is not connected",
			})
		}

		// A refusal worded for the operator by construction (a malformed
		// target_device, an image that cannot be installed, a parameter of the
		// wrong type): its text is the answer, as a 400. Anything NOT carrying
		// the marker stays the generic 500 below, which never echoes internals.
		var refusal *app_errors.OperatorRefusalError
		if errors.As(err, &refusal) {
			return results.SendValidationError(c, refusal.Error())
		}

		// Check if error indicates node is not online
		if len(errStr) > 20 && errStr[:20] == "node is not online" {
			return results.SendValidationError(c, errStr)
		}

		// Check if error indicates unknown command
		if len(errStr) > 15 && errStr[:15] == "unknown command" {
			return results.SendValidationError(c, errStr)
		}

		return results.SendInternalServerError(c, "failed to send command", err)
	}

	// The answer says what actually happened. A queueable command for an
	// offline node is registered and waits; calling that "sent" told the
	// operator something had reached the device when nothing had.
	delivery, message := "dispatched", "command dispatched to the node"
	if item.Queued {
		delivery, message = "queued", "command queued; it is delivered when the node reconnects"
	}
	data := map[string]string{
		"message":  message,
		"delivery": delivery,
	}
	if item.CommandID != nil {
		data["command_id"] = item.CommandID.String()
	}
	return results.SendOKResponse(c, data, message)
}

// commandFailureForLog marks, for the log level only, the refusals SendCommand
// answers with a 4xx although the error carries no status of its own: a
// hostname the device would reject, and an unknown command. Typed refusals are
// classified by results.LogFailure as they are.
func commandFailureForLog(err error) error {
	if errors.Is(err, nodeCommandDomain.ErrInvalidHostname) || strings.HasPrefix(err.Error(), "unknown command") {
		return app_errors.OperatorRefusal(err)
	}
	return err
}

// dispatchOne sends a command to one node and returns its batch item, or the
// item's failure with its TYPE intact (NodeCommandResultItem.FailureError):
// the arms in SendCommand match on it, and a typed refusal rebuilt as a string
// would flatten into the generic 500.
func (h *NodeHandler) dispatchOne(ctx context.Context, cmd node_command.NodeCommandCommand) (*node_command.NodeCommandResultItem, error) {
	batch, err := h.nodeService.SendBatchCommand(ctx, node_command.BatchNodeCommandCommand{
		NodeIDs:     []uuid.UUID{cmd.NodeID},
		Command:     cmd.Command,
		Params:      cmd.Params,
		AuthContext: cmd.AuthContext,
	})
	if err != nil {
		return nil, err
	}
	if batch == nil || len(batch.Results) == 0 {
		return nil, errors.New("no result returned for node command")
	}
	item := batch.Results[0]
	if !item.Success {
		return nil, item.FailureError()
	}
	return &item, nil
}

// SendBatchCommand sends a command to multiple nodes
// @Summary Send command to multiple nodes
// @Description Sends a command (reboot, shutdown, status_check, etc.) to multiple nodes
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param command body node_command.BatchNodeCommandCommand true "Batch command data (node_ids + slug + params)"
// @Success 200 {object} results.APIResponse{data=node_command.BatchCommandResult} "Commands sent successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/commands [post]
func (h *NodeHandler) SendBatchCommand(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse request body into batch command
	var cmd node_command.BatchNodeCommandCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request format")
	}

	// Bound and deduplicate the target list BEFORE the authorization loop below:
	// that loop costs one node query per supplied id, so a cap enforced only in
	// the service — after the loop — is paid for once per element of the request
	// body rather than once per node the batch may reach.
	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	// Derive the action from the slug (fail closed) and authorize it against
	// every target node's OU scope; a single denial rejects the whole batch.
	action, ok := authz.ActionForCommand(cmd.Command)
	if !ok {
		return results.SendValidationError(c, "unknown command")
	}
	for _, nodeID := range cmd.NodeIDs {
		if err := h.authorizeNodeAction(c, action, nodeID); err != nil {
			return err
		}
	}

	// Set auth context
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Send batch command via service
	batchResult, err := h.nodeService.SendBatchCommand(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to send batch command", err,
			"account_id", accountID, "command", cmd.Command, "node_count", len(cmd.NodeIDs))
		return results.SendInternalServerError(c, "failed to send batch command", err)
	}

	return results.SendOKResponse(c, batchResult, "batch command processed")
}

// ScanOSUsers dispatches an OS-user inventory scan to a node.
// @Summary Scan the OS accounts on a node
// @Description Dispatches a fixed read-only script that lists the device's real OS login accounts (distinct from Orchestrator-managed access entries). Returns a command id to poll with GET /os-users/scan/{commandId}. Online-only.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Scan dispatched; command_id to poll"
// @Failure 400 {object} results.APIResponse "Node offline or agent too old"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/os-users/scan [post]
func (h *NodeHandler) ScanOSUsers(c echo.Context) error {
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
	// Reading who is on the disk is a credentials-grade disclosure.
	if err := h.authorizeNodeAction(c, authz.ActionNodeCredentials, nodeID); err != nil {
		return err
	}
	commandID, err := h.nodeService.ScanOSUsers(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	return results.SendOKResponse(c, map[string]string{"command_id": commandID.String()}, "os-user scan dispatched")
}

// GetOSUserScan returns the parsed result of a dispatched OS-user scan.
// @Summary Poll an OS-user scan result
// @Description Returns the parsed OS-account inventory once the agent reports (status running/completed/failed). Raw command output is parsed server-side and never returned.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param commandId path string true "Scan command ID"
// @Success 200 {object} results.APIResponse{data=results.OSUserScanResult} "Scan status and parsed users"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node or command not found"
// @Router /api/v1/nodes/{id}/os-users/scan/{commandId} [get]
func (h *NodeHandler) GetOSUserScan(c echo.Context) error {
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
	if err := h.authorizeNodeAction(c, authz.ActionNodeCredentials, nodeID); err != nil {
		return err
	}
	res, err := h.nodeService.GetOSUserScanResult(c.Request().Context(), accountID, nodeID, commandID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node or command not found")
		}
		return results.SendNotFoundError(c, "node or command not found")
	}
	return results.SendOKResponse(c, res, "os-user scan result")
}

// ScanEEPROMConfig dispatches a live EEPROM-config read to a node.
// @Summary Scan the live EEPROM config on a node
// @Description Dispatches a fixed read of the device's actually-flashed rpi-eeprom-config (BOOT_ORDER, NET_INSTALL_*, HTTP_HOST/PATH). Returns a command id to poll. Online-only, RPi only.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Scan dispatched; command_id to poll"
// @Failure 400 {object} results.APIResponse "Node offline or agent too old"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Router /api/v1/nodes/{id}/eeprom-config/scan [post]
func (h *NodeHandler) ScanEEPROMConfig(c echo.Context) error {
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
	if err := h.authorizeNodeAction(c, authz.ActionNodeEEPROMFlash, nodeID); err != nil {
		return err
	}
	commandID, err := h.nodeService.ScanEEPROMConfig(c.Request().Context(), accountID, userID, nodeID)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	return results.SendOKResponse(c, map[string]string{"command_id": commandID.String()}, "eeprom-config scan dispatched")
}

// GetEEPROMConfigScan returns the parsed result of a dispatched EEPROM-config scan.
// @Summary Poll an EEPROM-config scan result
// @Description Returns the parsed live EEPROM config once the agent reports (status running/completed/failed).
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param commandId path string true "Scan command ID"
// @Success 200 {object} results.APIResponse{data=results.EEPROMConfigScanResult} "Scan status and parsed config"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node or command not found"
// @Router /api/v1/nodes/{id}/eeprom-config/scan/{commandId} [get]
func (h *NodeHandler) GetEEPROMConfigScan(c echo.Context) error {
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
	if err := h.authorizeNodeAction(c, authz.ActionNodeEEPROMFlash, nodeID); err != nil {
		return err
	}
	res, err := h.nodeService.GetEEPROMConfigScanResult(c.Request().Context(), accountID, nodeID, commandID)
	if err != nil {
		return results.SendNotFoundError(c, "node or command not found")
	}
	return results.SendOKResponse(c, res, "eeprom-config scan result")
}

// GetFiles lists all files for a node (latest version of each path)
// @Summary List files for node
// @Description Retrieves a list of all files for the specified node (latest version of each path)
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.TrackedFileListResult} "Files retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/files [get]
func (h *NodeHandler) GetFiles(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr // OU-scoped read
	}

	// Verify node belongs to account
	_, err = h.nodeService.GetByID(c.Request().Context(), node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	// The list is the tracked files, not "whatever has version rows". A failed
	// command used to be enough to put a path here for good, with no way to
	// remove it.
	tracked, err := h.nodeService.ListTrackedFiles(c.Request().Context(), nodeID)
	if err != nil {
		h.logger.Error("failed to list tracked files", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to get files", err)
	}

	// One query for every path's newest version, and no content: the loop used to
	// call GetLatest per row, which decrypted each whole file to read four
	// scalars — and rendered a row it could not decrypt as one that had never
	// been read off the device.
	meta, err := h.nodeFileService.ListLatestVersionMeta(c.Request().Context(), nodeID)
	if err != nil {
		h.logger.Error("failed to list file version metadata", "error", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to get files", err)
	}
	byPath := make(map[string]node_fileDomain.VersionMeta, len(meta))
	for _, m := range meta {
		byPath[m.Path] = m
	}

	rows := make([]results.TrackedFileResult, 0, len(tracked))
	for _, t := range tracked {
		var latest *node_fileDomain.VersionMeta
		if m, ok := byPath[t.Path]; ok {
			latest = &m
		}
		rows = append(rows, results.NewTrackedFileResult(t, latest))
	}
	return results.SendOKResponse(c, &results.TrackedFileListResult{Files: rows}, "files retrieved successfully")
}

// GetFile gets the latest version of a file
// @Summary Get file
// @Description Retrieves the latest version of a file for the specified node and path
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param path query string true "Absolute file path on the device, e.g. /boot/firmware/config.txt"
// @Param version query int false "Version number; omit for the latest"
// @Success 200 {object} results.APIResponse{data=results.FileResult} "File retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node or file not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/files/content [get]
func (h *NodeHandler) GetFile(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	path := c.QueryParam("path")
	if path == "" {
		return results.SendValidationError(c, "path is required")
	}

	// Reading stored content is a privileged read: a tracked file can be a
	// netplan config carrying a PSK or an application .env. The two platform
	// config.txt paths keep the node-read level they have always had, so this
	// does not regress the overclock/boot views.
	contentAction := authz.ActionNodeCredentials
	if node_fileDomain.IsPlatformPath(path) {
		contentAction = authz.ActionNodeRead
	}
	if aerr := h.authorizeNodeAction(c, contentAction, nodeID); aerr != nil {
		return aerr // OU-scoped
	}

	// Verify node belongs to account
	_, err = h.nodeService.GetByID(c.Request().Context(), node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	// A specific version when asked for, the latest otherwise. Without this the
	// editor showed the latest content while Apply sent the selected version —
	// the reviewed bytes and the applied bytes were different files.
	var file *node_fileDomain.NodeFile
	if v := c.QueryParam("version"); v != "" {
		version, verr := strconv.Atoi(v)
		if verr != nil || version < 1 {
			return results.SendValidationError(c, "invalid version")
		}
		file, err = h.nodeFileService.GetByVersion(c.Request().Context(), nodeID, path, version)
	} else {
		file, err = h.nodeFileService.GetLatest(c.Request().Context(), nodeID, path)
	}
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "file not found")
		}
		h.logger.Error("failed to get file", "error", err, "node_id", nodeID, "path", path)
		return results.SendInternalServerError(c, "failed to get file", err)
	}

	fileResult := results.NewFileResult(file)
	return results.SendOKResponse(c, fileResult, "file retrieved successfully")
}

// GetFileVersions gets all versions of a file
// @Summary Get file versions
// @Description Retrieves all versions of a file for the specified node and path
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param path query string true "Absolute file path on the device, e.g. /boot/firmware/config.txt"
// @Success 200 {object} results.APIResponse{data=results.FileVersionsResult} "File versions retrieved successfully (metadata only — content comes from /files/content)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/files/versions [get]
func (h *NodeHandler) GetFileVersions(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	path := c.QueryParam("path")
	if path == "" {
		return results.SendValidationError(c, "path is required")
	}

	if aerr := h.authorizeNodeAction(c, authz.ActionNodeRead, nodeID); aerr != nil {
		return aerr // OU-scoped read
	}

	// Verify node belongs to account
	_, err = h.nodeService.GetByID(c.Request().Context(), node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	// Get all versions
	files, err := h.nodeFileService.GetAllVersions(c.Request().Context(), nodeID, path)
	if err != nil {
		h.logger.Error("failed to get file versions", "error", err, "node_id", nodeID, "path", path)
		return results.SendInternalServerError(c, "failed to get file versions", err)
	}

	// Convert to results
	fileVersionsResult := results.NewFileVersionsResult(files)
	return results.SendOKResponse(c, fileVersionsResult, "file versions retrieved successfully")
}

// UpdateFile saves new content of a tracked file as a new version. Nothing is
// sent to the node: applying a saved version is a separate step (ApplyFile).
// @Summary Update file
// @Description Saves new content of a tracked file as a new version with status "saved". Nothing is sent to the node; apply the version with POST /api/v1/nodes/{id}/files/{file_id}/apply, where file_id is the id this response returns. Saving content identical to the newest version stores nothing and returns that version.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body node_file_update.NodeFileUpdateCommand true "File update request; the path travels in the body, never as a route segment"
// @Success 200 {object} results.APIResponse{data=results.FileUpdateResult} "File version saved (status saved), not yet applied"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Node not found, or the file is not tracked for it"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/files/content [put]
func (h *NodeHandler) UpdateFile(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	// Writing a node config file is an RCE-equivalent root write.
	if err := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); err != nil {
		return err
	}

	// Parse request body — the path travels in the body now, not in a wildcard
	// route segment (see the route comment).
	var cmd node_file_update.NodeFileUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request format")
	}

	cmd.NodeID = nodeID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)
	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}
	path := cmd.Path

	// Verify node belongs to account
	_, err = h.nodeService.GetByID(c.Request().Context(), node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	// Create the version through the node service: it is the layer that knows
	// whether the file is tracked and whether the node has room for it.
	file, err := h.nodeService.SaveNodeFileVersion(c.Request().Context(), accountID, cmd.NodeID, cmd.Path, cmd.Content)
	if err != nil {
		return h.mapFileError(c, err)
	}

	fileUpdateResult := results.NewFileUpdateResult(file.ID, path, file.Version, file.Status, "file version created successfully")
	return results.SendOKResponse(c, fileUpdateResult, "file version created successfully")
}

// ApplyFile applies a saved file version to the agent
// @Summary Apply file version to agent
// @Description Applies a saved file version to the agent by updating status to "applying" and sending command
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param file_id path string true "File version ID"
// @Success 200 {object} results.APIResponse{data=results.FileUpdateResult} "File applied successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "The file is written by the node's access configuration (sudo grants, SSH key, principal and CA files, the platform's sshd drop-ins) and cannot be applied as a tracked file"
// @Failure 404 {object} results.APIResponse "Node or file not found"
// @Failure 503 {object} results.APIResponse "Node not connected"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/files/{file_id}/apply [post]
func (h *NodeHandler) ApplyFile(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.authorizeNodeAction(c, authz.ActionNodeFileWrite, nodeID); err != nil {
		return err
	}

	fileIDStr := c.Param("file_id")
	fileID, err := uuid.Parse(fileIDStr)
	if err != nil {
		h.logger.Warn("invalid file ID", "error", err, "file_id", fileIDStr)
		return results.SendValidationError(c, "invalid file ID")
	}

	// Verify node belongs to account
	_, err = h.nodeService.GetByID(c.Request().Context(), node_get.NodeGetQuery{
		ID:          nodeID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to get node", err)
	}

	// Get file to verify it belongs to the node
	file, err := h.nodeFileService.GetByID(c.Request().Context(), fileID)
	if err != nil {
		h.logger.Error("failed to get file", "error", err, "file_id", fileID)
		return results.SendNotFoundError(c, "file not found")
	}

	// Verify file belongs to the node
	if file.NodeID != nodeID {
		return results.SendNotFoundError(c, "file not found")
	}

	// Apply file version (updates status to "applying")
	appliedFile, err := h.nodeFileService.ApplyFileVersion(c.Request().Context(), fileID)
	if errors.Is(err, node_fileDomain.ErrAccessManagedPath) {
		return h.mapFileError(c, err)
	}
	if err != nil {
		h.logger.Error("failed to apply file version", "error", err, "file_id", fileID)
		return results.SendInternalServerError(c, "failed to apply file version", err)
	}

	// Send command to agent
	if err := h.nodeService.SendUpdateFileCommand(c.Request().Context(), fileID, accountID); err != nil {
		h.logger.Error("failed to send file update command", "error", err, "file_id", fileID)

		// Check error type and return appropriate HTTP status
		errStr := err.Error()
		if strings.Contains(errStr, "not connected") {
			return results.SendErrorWithCode(c, http.StatusServiceUnavailable, "node is not connected", "service_unavailable")
		}
		if strings.Contains(errStr, "not online") {
			return results.SendValidationError(c, errStr)
		}

		return results.SendInternalServerError(c, "failed to send file update command", err)
	}

	fileUpdateResult := results.NewFileUpdateResult(appliedFile.ID, appliedFile.Path, appliedFile.Version, appliedFile.Status, "file applied successfully")
	return results.SendOKResponse(c, fileUpdateResult, "file applied successfully")
}

// Approve approves a pending node (for master token authentication)
// @Summary Approve pending node
// @Description Approves a node that is pending approval (registered via master token). A pending node takes no commands; on approval of a node running its installed OS the platform sends it the access configuration and utilities that reach it, when any are due. A node running the recovery system from a provisioning card also has its bootloader rewritten to boot from this server first, when its effective network-first policy is on (the default); a node running its own operating system, from any medium, keeps its bootloader. The node's boot target is not changed: set it (PUT /api/v1/nodes/{id}/policy) before approving if the inherited one is not what the device should boot.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node approved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 409 {object} results.APIResponse "The install is at its device cap (code device_cap_reached). data: cap, account_devices (the caller's own admitted count) and instance_operator; the instance operator also gets count (install-wide) and subscription_url. Or the account is at its own device limit (code account_device_limit_reached; data: limit, account_devices, plan, until for a dated subscription, contact). The node stays pending"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 503 {object} results.APIResponse "The device cap could not be checked (code device_cap_unverified): nothing was written; try again"
// @Router /api/v1/nodes/{id}/approve [post]
func (h *NodeHandler) Approve(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.authorizeNodeAction(c, authz.ActionNodeApprove, nodeID); err != nil {
		return err
	}

	actorUserID, _ := middleware.GetUserIDFromContext(c)
	node, err := h.nodeService.ApproveNode(c.Request().Context(), accountID, actorUserID, nodeID)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to approve node", err, "node_id", nodeID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		if errors.Is(err, app_errors.ErrNodeAlreadyApproved) {
			return results.SendValidationError(c, "node is already approved")
		}
		return results.SendInternalServerError(c, "failed to approve node", err)
	}

	// Network-first onboarding: if the node's effective policy opts into it and the
	// device is running the recovery system from a provisioning card (so there is
	// a writable boot partition to stage recovery.bin on), automatically flash the
	// network-first EEPROM now. A device running its own operating system is never
	// flashed here, whatever it boots from. Best-effort — a failure here must not
	// fail the approval.
	if userID, uerr := middleware.GetUserIDFromContext(c); uerr == nil {
		// Best-effort — a failure here must not fail the approval.
		if _, perr := h.eepromProvision.MaybeProvisionNetworkFirst(c.Request().Context(), accountID, userID, node, h.platformURL(c), h.grpcEndpoint()); perr != nil {
			h.logger.Error("network-first auto-provision failed on approve", "node_id", node.ID, "error", perr)
		}
	}

	nodeResult := results.NewNodeResult(node)
	return results.SendOKResponse(c, nodeResult, "node approved successfully")
}

// platformURL / grpcEndpoint mirror the provisioning handler: prefer the configured
// advertisement values, else fall back to the request host.
func (h *NodeHandler) platformURL(c echo.Context) string {
	if h.config.Server.AdvertisementURL != "" {
		return h.config.Server.AdvertisementURL
	}
	// c.Scheme() honors X-Forwarded-Proto (Request().TLS is nil behind the
	// TLS-terminating proxy).
	return c.Scheme() + "://" + c.Request().Host
}

func (h *NodeHandler) grpcEndpoint() string {
	if h.config.GRPC.AdvertisementURL != "" {
		return h.config.GRPC.AdvertisementURL
	}
	return net.JoinHostPort(h.config.GRPC.Host, fmt.Sprintf("%d", h.config.GRPC.Port))
}

// ProvisionEEPROMRequest is the optional body of POST /nodes/:id/provision-eeprom.
type ProvisionEEPROMRequest struct {
	// BootOrder overrides the baked BOOT_ORDER (first element tried first).
	// Empty = network-first default ["http","nvme","usb_msd","sd","restart"]
	// (0xf1467).
	BootOrder []string `json:"boot_order"`
}

// ProvisionEEPROM (re)flashes the per-device EEPROM on demand, decoupled from
// approval: reprovision after fixing the OU/policy, change the baked boot
// order, or roll the bootloader firmware forward (the signer always bakes from
// its latest firmware base).
// @Summary Provision per-device EEPROM (netboot identity / boot order / bootloader update)
// @Description Signs a fresh per-device EEPROM image (latest firmware base, serial baked into HTTP_PATH, adoption token, requested boot order) and commands the online node to flash it. Where the node's system has rpi-eeprom-update (a managed OS) the flash is direct (flashrom), does not reboot the node, and needs a reboot to take effect; on the recovery system (netbooted or from an SD card), or an OS without that tool, the package is staged on the SD/USB boot partition and the node reboots at once to apply it. A netbooted node without SD/USB media cannot flash and the command fails.
// @Tags nodes
// @Accept json
// @Produce json
// @Param id path string true "Node ID"
// @Param request body ProvisionEEPROMRequest false "Optional boot order override (default http,nvme,sd,restart)"
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]string} "EEPROM flash triggered"
// @Failure 400 {object} results.APIResponse "Bad request"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent and executes no commands, or has not reported its hardware serial yet (code no_hardware_serial)"
// @Router /api/v1/nodes/{id}/provision-eeprom [post]
func (h *NodeHandler) ProvisionEEPROM(c echo.Context) error {
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

	if err := h.authorizeNodeAction(c, authz.ActionNodeEEPROMFlash, nodeID); err != nil {
		return err
	}

	// After the authz gate, not before it: whether this install runs a signer
	// is a deployment fact, and a removed member asking got it back as a 500
	// instead of the 401 every other route on this handler answers with.
	if h.signer == nil {
		return results.SendInternalServerError(c, "EEPROM signer is not configured", fmt.Errorf("nil signer"))
	}

	var req ProvisionEEPROMRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	bootOrder := req.BootOrder
	if len(bootOrder) == 0 {
		bootOrder = nodeDomain.NetworkFirstBootOrder
	}
	if _, err := nodeDomain.BootOrderToHex(bootOrder); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	node, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), nodeID, accountID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to load node", err)
	}

	if err := h.eepromProvision.ProvisionNetworkFirstEEPROM(c.Request().Context(), accountID, userID, node, bootOrder, h.platformURL(c), h.grpcEndpoint()); err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "provision-eeprom failed", err, "node_id", nodeID)
		return results.SendInternalServerError(c, "failed to provision EEPROM", err)
	}
	// Default the node to boot its OS so the apply-reboot lands on NVMe instead
	// of netbooting into recovery on every boot (best-effort; explicit overrides
	// are respected). A node in the recovery system with no OS install on
	// record keeps inheriting: it has no OS to boot.
	if err := h.nodeService.DefaultBootToOSOn(c.Request().Context(), nodeID, accountID); err != nil {
		h.logger.Warn("could not default boot_to_os on after provision", "node_id", nodeID, "error", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "flashing"}, "per-device EEPROM flash triggered")
}

// webusbSerialRe bounds a WebUSB-harvested board serial: plain hex, USB-short
// (8) to full RPi (16) length. Anything else is rejected before it can reach
// serial-suffix matching or signer package paths.
var webusbSerialRe = regexp.MustCompile(`^[0-9a-f]{8,16}$`)

// GetBaseFirmware returns the bootloader firmware base the signer currently
// bakes per-device EEPROM images from, keyed by SoC family.
// @Summary Get bootloader firmware base per SoC
// @Description Returns the newest bootloader firmware (version hash, build timestamp, image name) the EEPROM signer would bake from, keyed by SoC ("cm4"/"cm5"). Used by the UI to tell whether a node's running bootloader is up to date. Empty object when the signer is not configured.
// @Tags nodes
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]eepromsigner.BaseFirmware} "Firmware base per SoC"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 502 {object} results.APIResponse "Signer unreachable"
// @Router /api/v1/eeprom/base-firmware [get]
func (h *NodeHandler) GetBaseFirmware(c echo.Context) error {
	if h.signer == nil {
		return results.SendOKResponse(c, map[string]eepromsigner.BaseFirmware{}, "EEPROM signer is not configured")
	}

	fw, stale, err := h.baseFW.Versions(c.Request().Context())
	if err != nil {
		h.logger.Error("failed to fetch signer firmware base", "error", err)
		return results.SendError(c, http.StatusBadGateway, "EEPROM signer unreachable", err)
	}
	if stale {
		// Serve the stale cache rather than flapping the UI.
		return results.SendOKResponse(c, fw, "bootloader firmware base (stale cache)")
	}
	return results.SendOKResponse(c, fw, "bootloader firmware base")
}

// Reject rejects a pending node (for master token authentication)
// @Summary Reject pending node
// @Description Rejects a node that is pending approval (registered via master token)
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeResult} "Node rejected successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/reject [post]
func (h *NodeHandler) Reject(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	nodeIDStr := c.Param("id")
	nodeID, err := uuid.Parse(nodeIDStr)
	if err != nil {
		h.logger.Warn("invalid node ID", "error", err, "node_id", nodeIDStr)
		return results.SendValidationError(c, "invalid node ID")
	}

	if err := h.authorizeNodeAction(c, authz.ActionNodeApprove, nodeID); err != nil {
		return err
	}

	actorUserID, _ := middleware.GetUserIDFromContext(c)
	node, err := h.nodeService.RejectNode(c.Request().Context(), accountID, actorUserID, nodeID)
	if err != nil {
		h.logger.Error("failed to reject node", "error", err, "node_id", nodeID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		if errors.Is(err, app_errors.ErrNodeNotPendingApproval) {
			return results.SendValidationError(c, "node is not pending approval")
		}
		return results.SendInternalServerError(c, "failed to reject node", err)
	}

	nodeResult := results.NewNodeResult(node)
	return results.SendOKResponse(c, nodeResult, "node rejected successfully")
}

// RebootToRecovery reboots a node into the network bootstrap once
// @Summary Reboot node to recovery
// @Description One-shot netboot: forces the boot target to the network bootstrap, reboots the node, and restores the previous target once it reconnects netbooted
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Reboot to recovery initiated"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 409 {object} results.APIResponse "Refused before anything changed: the node is not online (node_unavailable), its boot order cannot reach HTTP boot or its bootloader fetches from a different boot server (recovery_unreachable), or it runs the watcher agent (watcher_node)"
// @Router /api/v1/nodes/{id}/reboot-to-recovery [post]
func (h *NodeHandler) RebootToRecovery(c echo.Context) error {
	// Hard capability gate: without a netboot chain the call would still pin
	// boot_to_os off + set revert-pending, reboot the device into its local OS,
	// and the revert condition (a reconnect reporting current_boot_device=http)
	// would never fire — silently corrupting the node's boot policy. Refuse
	// up-front on installs that do not serve network-boot files.
	if h.config.Provisioning.HTTPBootDir == "" {
		return results.SendValidationError(c, "network boot is not configured on this server")
	}
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
	if err := h.authorizeNodeAction(c, authz.ActionNodeRebootRecovery, nodeID); err != nil {
		return err
	}
	// A device whose bootloader points at a different boot server is refused
	// by the service (409 recovery_unreachable), which the playbook block
	// shares with this route.
	if err := h.nodeService.RebootToRecovery(c.Request().Context(), accountID, userID, nodeID); err != nil {
		h.logger.Error("failed to reboot node to recovery", "error", err, "node_id", nodeID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to reboot node to recovery", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "initiated"}, "reboot to recovery initiated")
}

// ClearBootFailsafe acknowledges and clears a node's boot-loop failsafe flag.
// @Summary Clear the boot-loop failsafe
// @Description Clears the "auto-recovered" boot-loop failsafe flag and the withheld-attempt counter for a node (operator acknowledge).
// @Tags nodes
// @Produce json
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse
// @Failure 400 {object} results.APIResponse
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Security BearerAuth
// @Router /api/v1/nodes/{id}/clear-boot-failsafe [post]
func (h *NodeHandler) ClearBootFailsafe(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	if err := h.authorizeNodeAction(c, authz.ActionNodeUpdate, nodeID); err != nil {
		return err
	}
	if err := h.nodeService.ClearBootFailsafe(c.Request().Context(), accountID, nodeID); err != nil {
		h.logger.Error("failed to clear boot failsafe", "error", err, "node_id", nodeID)
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to clear boot failsafe", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "cleared"}, "boot failsafe cleared")
}

// GetBootcode streams the generic per-SoC second-stage recovery.bin for the
// WebUSB flasher's serial-harvest phase.
// @Summary Get generic second-stage bootcode (WebUSB serial harvest)
// @Description Returns the generic (non-per-device) recovery.bin for a SoC ("cm4"/"cm5") — the bytes a browser WebUSB flasher pushes to a bare board in usbboot mode to harvest its serial before baking a per-serial EEPROM.
// @Tags nodes
// @Produce application/octet-stream
// @Security ApiKeyAuth
// @Param soc path string true "SoC family (cm4|cm5)"
// @Success 200 {file} binary "recovery.bin"
// @Failure 400 {object} results.APIResponse "Invalid SoC"
// @Failure 500 {object} results.APIResponse "Signer not configured / error"
// @Router /api/v1/eeprom/bootcode/{soc} [get]
func (h *NodeHandler) GetBootcode(c echo.Context) error {
	if h.signer == nil {
		return results.SendInternalServerError(c, "EEPROM signer is not configured", fmt.Errorf("nil signer"))
	}
	soc := c.Param("soc")
	if soc != "cm4" && soc != "cm5" {
		return results.SendValidationError(c, "invalid soc (cm4|cm5)")
	}
	data, err := h.signer.Bootcode(c.Request().Context(), soc)
	if err != nil {
		h.logger.Error("failed to fetch bootcode", "soc", soc, "error", err)
		return results.SendInternalServerError(c, "failed to fetch bootcode", err)
	}
	return c.Blob(http.StatusOK, "application/octet-stream", data)
}

// WebUSBProvisionRequest is the body of POST /eeprom/webusb-provision.
type WebUSBProvisionRequest struct {
	SoC             string `json:"soc"`    // cm4 | cm5 (from the USB device id)
	Serial          string `json:"serial"` // harvested over WebUSB
	NodeName        string `json:"node_name"`
	DefaultUsername string `json:"default_username"`
	DefaultPassword string `json:"default_password"`
}

// WebUSBProvisionResponse tells the browser where to fetch the signed package.
type WebUSBProvisionResponse struct {
	NodeID      string   `json:"node_id"`
	NodeName    string   `json:"node_name"`
	PackageBase string   `json:"package_base"` // e.g. /net_install/eeprom/by-serial/<serial>
	Files       []string `json:"files"`
	// SigQuery is the short-lived HMAC capability the download endpoint requires
	// for a per-device (token-bearing) package, e.g. "?exp=...&sig=...". The
	// browser appends it to each `PackageBase/<file>` request.
	SigQuery string `json:"sig_query"`
}

// WebUSBProvision creates-or-matches a node for a harvested board serial and
// bakes a per-serial network-first EEPROM carrying that node's adoption token,
// so a browser WebUSB flash makes the device boot network-first and self-adopt.
// @Summary Bake a per-serial EEPROM for the WebUSB flasher
// @Description Given a SoC + a serial harvested over WebUSB, creates or matches a node, bakes a network-first signed EEPROM carrying that node's adoption token, and returns where to fetch the package. The browser then flashes it; the device boots network-first and adopts as the node. Provisioning credentials are optional at flash time, and the server sets no default username or password: an omitted default_username leaves the node with no provisioning account (name one before running provision_connection), and an omitted default_password means no console password.
// @Tags nodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param request body WebUSBProvisionRequest true "SoC, serial and node defaults"
// @Success 200 {object} results.APIResponse{data=WebUSBProvisionResponse} "Signed package location"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "Hardware serial registered to another organization; the install is at its device cap (device_cap_reached) or the account at its own device limit (account_device_limit_reached); or the device connected while the re-flash was being prepared (device_connected_during_flash)"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Failure 503 {object} results.APIResponse "The device cap could not be checked (code device_cap_unverified): nothing was written; try again"
// @Router /api/v1/eeprom/webusb-provision [post]
func (h *NodeHandler) WebUSBProvision(c echo.Context) error {
	if h.signer == nil {
		return results.SendInternalServerError(c, "EEPROM signer is not configured", fmt.Errorf("nil signer"))
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Needed for the rollback delete further down: a node this handler created
	// and then removes is still a node the caller caused to disappear.
	webusbActorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var req WebUSBProvisionRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if req.SoC != "cm4" && req.SoC != "cm5" {
		return results.SendValidationError(c, "invalid soc (cm4|cm5)")
	}
	// The serial is stored on the node and later used in suffix matching and in
	// signer/package paths — restrict it to a plain hex serial (8-16 nibbles,
	// USB-short to full RPi serial) so it can't act as a wildcard or a path.
	req.Serial = strings.ToLower(strings.TrimSpace(req.Serial))
	if !webusbSerialRe.MatchString(req.Serial) {
		return results.SendValidationError(c, "invalid serial (expect 8-16 hex characters)")
	}

	// Match an existing node by hardware serial, else create a fresh one.
	//
	// ResolveSerialForBake, not GetByHardwareSerial: the bake below writes a
	// package carrying THIS account's adoption token into a path keyed on the
	// serial alone, so the cross-account guard has to run for the serial that
	// was asked for, before any node is matched. A bare GetByHardwareSerial
	// matched a short stored stamp as the suffix of a full query serial, which
	// reached the existing-node branch for a serial the caller does not own —
	// skipping the guard that only the create branch applies.
	var nodeID uuid.UUID
	var nodeName string
	existing, err := h.nodeService.ResolveSerialForBake(c.Request().Context(), accountID, req.Serial)
	switch {
	case err == nil:
		nodeID = existing.ID
		nodeName = existing.Name
		// Re-flashing a not-yet-adopted placeholder: put it back into the
		// flashing state so the list shows "preparing" until this flash commits.
		// A node that has already connected keeps its real (live) status.
		// The re-mark is conditional in SQL on the row still awaiting its
		// first connect: a device that connected after the read above is
		// refused with a typed 409 rather than moved out of the device cap's
		// count while its stream stays up. Any other failure stays cosmetic
		// (the list's "preparing" badge), as before.
		if existing.AwaitingFirstConnect {
			if uerr := h.nodeService.MarkWebUSBReflashing(c.Request().Context(), nodeID, accountID); uerr != nil {
				var connected *node_service.WebUSBDeviceConnectedError
				if errors.As(uerr, &connected) {
					return results.SendInternalServerError(c, "failed to prepare the flash", uerr)
				}
				h.logger.Warn("failed to re-mark node flashing", "node_id", nodeID, "error", uerr)
			}
		}
	case errors.Is(err, app_errors.ErrHardwareSerialConflict):
		return results.SendError(c, http.StatusConflict,
			"this device's hardware serial is already registered to another organization; if you recently acquired this device, ask the previous owner to remove it from their account, or contact support", nil)
	case errors.Is(err, app_errors.ErrNodeNotFound):
		name := strings.TrimSpace(req.NodeName)
		if name == "" {
			name = "Node-" + req.Serial
		}
		nodeName = name
		// Provisioning creds are optional at flash time: the device adopts via
		// its token, and this path sends no credentials anywhere. The server
		// sets no default username or password. An empty username is a node
		// nothing will provision until an operator names the account, which is
		// the honest state for a flash that carried no login at all.
		username := strings.TrimSpace(req.DefaultUsername)
		password := req.DefaultPassword
		created, cerr := h.nodeService.Create(c.Request().Context(), node_create.NodeCreateCommand{
			ID:                    uuid.New(),
			Name:                  name,
			DefaultUsername:       username,
			DefaultPassword:       password,
			ProvisionNetworkFirst: true,
			AuthContext:           commands.NewAuthContext(accountID, userID),
		})
		if cerr != nil {
			h.logger.Error("failed to create node for webusb provision", "error", cerr)
			return results.SendInternalServerError(c, "failed to create node", cerr)
		}
		nodeID = created.ID
		// Stamp the placeholder with the short serial + awaiting-first-connect so
		// it drives the "flashed, awaiting boot" UI, answers the HTTP-boot serve
		// decision, and reconciles into one node when the device connects.
		if serr := h.nodeService.StampWebUSBPlaceholder(c.Request().Context(), nodeID, accountID, req.Serial); serr != nil {
			if errors.Is(serr, app_errors.ErrHardwareSerialConflict) {
				// Refuse to mint a signed EEPROM for a serial owned elsewhere, and
				// remove the just-created node so no fingerprint-less orphan remains.
				// force: a rollback of a placeholder created moments ago. It has
				// no device behind it, so the online gate cannot apply — but a
				// rollback that could be refused would leave the orphan it exists
				// to remove.
				if derr := h.nodeService.Delete(c.Request().Context(), accountID, webusbActorUserID, nodeID, true); derr != nil {
					h.logger.Warn("failed to delete node after serial conflict", "node_id", nodeID, "error", derr)
				}
				return results.SendError(c, http.StatusConflict,
					"this device's hardware serial is already registered to another organization; if you recently acquired this device, ask the previous owner to remove it from their account, or contact support", nil)
			}
			h.logger.Warn("failed to stamp webusb placeholder", "node_id", nodeID, "error", serr)
		}
		// The placeholder is mid-flash: hide it behind the "flashing" status until
		// the browser confirms the EEPROM write via the commit endpoint. An
		// interrupted flash leaves the node visibly in this state as a retry cue.
		if uerr := h.nodeService.UpdateStatusWithAccount(c.Request().Context(), nodeID, accountID, string(nodeDomain.StatusFlashing)); uerr != nil {
			h.logger.Warn("failed to mark node flashing", "node_id", nodeID, "error", uerr)
		}
	default:
		return results.SendInternalServerError(c, "failed to look up node", err)
	}

	adoption, err := h.nodeService.GetAdoptionInfo(c.Request().Context(), accountID, nodeID, h.platformURL(c), h.grpcEndpoint())
	if err != nil {
		return results.SendInternalServerError(c, "failed to build adoption info", err)
	}
	hexOrder, err := nodeDomain.BootOrderToHex(nodeDomain.NetworkFirstBootOrder)
	if err != nil {
		return results.SendInternalServerError(c, "invalid boot order", err)
	}
	resp, err := h.signer.Sign(c.Request().Context(), eepromsigner.SignRequest{
		SoC:         req.SoC,
		Serial:      req.Serial,
		AdoptionB64: adoption,
		BootOrder:   hexOrder,
		NodeID:      nodeID.String(),
	})
	if err != nil {
		h.logger.Error("failed to sign webusb eeprom", "error", err, "serial", req.Serial)
		return results.SendInternalServerError(c, "failed to sign EEPROM", err)
	}

	// The capability is bound to THIS account and node, and the download
	// handler serves only that node's package (cdnsign.SignEEPROMPackage). The
	// public URL keeps the serial-only shape; the node rides in the signature.
	exp, sig := cdnsign.SignEEPROMPackage(req.Serial, accountID, nodeID, cdnsign.DeriveKey(h.config.CDNSigningSecret()), 15*time.Minute)
	return results.SendOKResponse(c, WebUSBProvisionResponse{
		NodeID:      nodeID.String(),
		NodeName:    nodeName,
		PackageBase: "/net_install/eeprom/by-serial/" + req.Serial,
		Files:       resp.Files,
		SigQuery:    "?" + cdnsign.ParamExpires + "=" + exp + "&" + cdnsign.ParamSignature + "=" + sig,
	}, "per-serial EEPROM signed")
}

// WebUSBProvisionCommit finalizes a WebUSB flash. The node was created in the
// "flashing" state at provision time (its adoption token had to be baked into
// the EEPROM before the write); the browser calls this once the EEPROM write
// completed to reveal it as ready_for_adoption. Idempotent — a node that has
// already advanced (e.g. the device connected first) is left untouched.
// @Summary Commit a WebUSB flash
// @Description Reveal a flashing node as ready_for_adoption after the EEPROM write completes.
// @Tags Nodes
// @Produce json
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse "Flash committed"
// @Failure 400 {object} results.APIResponse "Invalid node ID"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Security BearerAuth
// @Router /api/v1/eeprom/webusb-provision/{id}/committed [post]
func (h *NodeHandler) WebUSBProvisionCommit(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	if err := h.nodeService.CommitWebUSBFlash(c.Request().Context(), nodeID, accountID); err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to commit flash", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": string(nodeDomain.StatusReadyForAdoption)}, "flash committed")
}
