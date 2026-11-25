package organizational_unit

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_summary"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	ou_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// OrganizationalUnitHandler serves OU management and node membership/policy.
type OrganizationalUnitHandler struct {
	service  *ou_service.Service
	enforcer *authz_service.Enforcer
	audit    *audit.Recorder
	logger   *slog.Logger
	// nodeService serves the unit-wide device-profile apply — the dispatch
	// itself is the node service's ApplyDeviceProfile, per node.
	nodeService services.NodeService
}

// canReadSecretValues reports whether the caller may see plaintext effective
// secret/variable values (admin+); others get provenance + metadata only.
//
// A YES is RECORDED, from here rather than from each endpoint — this is the one
// place the decision is made, and the gap being closed was precisely that
// serving plaintext left no row.
func (h *OrganizationalUnitHandler) canReadSecretValues(c echo.Context, scope audit.SecretValueReadScope, targetType, subjectID string) bool {
	if middleware.Authorize(c, h.enforcer, authz.ActionSecretReadValue, nil) != nil {
		return false
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return false
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return false
	}
	h.audit.RecordSecretValuesRead(c.Request().Context(), audit.UserActor(accountID, userID),
		scope, targetType, subjectID)
	return true
}

func NewOrganizationalUnitHandler(service *ou_service.Service, auditRecorder *audit.Recorder, logger *slog.Logger, nodeService services.NodeService) *OrganizationalUnitHandler {
	// Required: the effective-secrets routes are where plaintext leaves the
	// platform through this handler, and a nil recorder would make that
	// invisible.
	if auditRecorder == nil {
		panic("OrganizationalUnitHandler requires an audit recorder")
	}
	return &OrganizationalUnitHandler{
		nodeService: nodeService,service: service, audit: auditRecorder, logger: logger.With("handler", "organizational_unit")}
}

func (h *OrganizationalUnitHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	h.enforcer = enforcer
	ouManage := middleware.RequireAction(enforcer, authz.ActionOUManage)
	nodeAssign := middleware.RequireAction(enforcer, authz.ActionNodeAssignOU)
	policyManage := middleware.RequireAction(enforcer, authz.ActionAccountPolicy)
	// The effective-secrets endpoints answer "which credentials does this node
	// (or this unit) receive". The secret handler gave plain enumeration its own
	// action for exactly this reason — a name is the {{secrets.<name>}}
	// identifier and the attachment map says which node holds which credential,
	// which is a reconnaissance map rather than metadata. These two serve the
	// same map, so they take the same action, and one role change moves both.
	secretRead := middleware.RequireAction(enforcer, authz.ActionSecretRead)
	// effective-policy does NOT leak the password (maskedPassword blanks the
	// value) — it returns boot flags, the
	// default username and which SSH key and image apply. Still gated, on the
	// action a viewer already holds: it costs nothing for real roles and makes
	// the per-action doctrine true here instead of declared. ActionNodeRead had
	// no enforcement site at all before this.
	nodeRead := middleware.RequireAction(enforcer, authz.ActionNodeRead)

	ous := e.Group("/api/v1/organizational-units")
	ous.Use(authMiddleware)
	// ActionOURead had no enforcement site either, for the same reason
	// ActionNodeRead did not — see the note above. The unit tree is the
	// product's information architecture, so an OU-scoped member reading it is
	// a real question, and it must be answered by an action rather than by
	// whoever holds a valid token.
	//
	// ou.read is an any-scope catalog read (authz.IsAnyScopeRead), so the list
	// middleware passes for an OU-scoped member too — the handler then narrows
	// the tree to their allowed subtree via AllowedOUs. The per-unit routes
	// authorize IN the handler against the unit itself (an OU-scoped binding
	// covers its own subtree), which is why they carry no action middleware:
	// the middleware form can only ask the nil-target question.
	ouRead := middleware.RequireAction(enforcer, authz.ActionOURead)
	ous.POST("", h.Create, ouManage)
	ous.GET("", h.List, ouRead)
	ous.GET("/:id", h.Get)
	ous.PUT("/:id", h.Update)
	ous.DELETE("/:id", h.Delete)
	ous.GET("/:id/effective-secrets", h.OUEffectiveSecrets)
	ous.GET("/:id/summary", h.Summary)
	// The deliberate fleet button: apply the unit's effective device profile to
	// every node assigned to the unit. Authorized in-handler against the unit,
	// like the other per-unit routes.
	ous.POST("/:id/device-profile/apply", h.ApplyDeviceProfileToUnit)

	// Node membership + policy live under the node's path but are served here.
	nodes := e.Group("/api/v1/nodes")
	nodes.Use(authMiddleware)
	nodes.PUT("/:id/organizational-unit", h.AssignNode, nodeAssign)
	nodes.PUT("/:id/policy", h.SetNodePolicy, nodeAssign)
	nodes.POST("/:id/policy/reset", h.ResetNodePolicy, nodeAssign)
	nodes.GET("/:id/effective-policy", h.EffectivePolicy, nodeRead)
	nodes.GET("/:id/effective-secrets", h.NodeEffectiveSecrets, secretRead)

	// Account-wide policy fallback (applies to unassigned nodes and any field
	// the OU chain leaves unresolved).
	accountPolicy := e.Group("/api/v1/account-policy")
	accountPolicy.Use(authMiddleware)
	// The account policy is the ROOT of the same node→OU→account chain the unit
	// tree carries, and it returns the account default username, ssh key, OS
	// image, network_first/boot_to_os/ssh_password_auth and the device and LED
	// profiles. It takes ou.read for that reason: the same any-scope catalog
	// read that opens the unit tree, held by every role down to viewer, so an
	// OU-scoped member keeps a working session while a non-member and a
	// removed member are refused.
	accountPolicy.GET("", h.GetAccountPolicy, ouRead)
	accountPolicy.PUT("", h.UpdateAccountPolicy, policyManage)
}

// GetAccountPolicy returns the account-wide policy fallback
// @Summary Get account default policy
// @Description Retrieves the account-wide policy fallback applied to unassigned nodes and unresolved fields. Requires ou.read, the same any-scope catalog read that opens the unit tree.
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.AccountPolicyResult} "Account policy retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/account-policy [get]
func (h *OrganizationalUnitHandler) GetAccountPolicy(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	policy, err := h.service.GetAccountPolicy(c.Request().Context(), auth.AccountID)
	if err != nil {
		h.logger.Error("failed to get account policy", "error", err, "account_id", auth.AccountID)
		return results.SendInternalServerError(c, "failed to get account policy", err)
	}
	return results.SendOKResponse(c, results.NewAccountPolicyResult(policy), "account policy retrieved successfully")
}

// UpdateAccountPolicy updates the account-wide policy fallback
// @Summary Update account default policy
// @Description Updates the account-wide policy fallback; nil fields are unchanged, empty/inherit clears a field
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param policy body account_policy_update.AccountPolicyUpdateCommand true "Policy fields to update"
// @Success 200 {object} results.APIResponse{data=results.AccountPolicyResult} "Account policy updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/account-policy [put]
func (h *OrganizationalUnitHandler) UpdateAccountPolicy(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var cmd account_policy_update.AccountPolicyUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.AuthContext = auth
	policy, err := h.service.SetAccountPolicy(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update account policy", err, "account_id", auth.AccountID)
		return results.SendInternalServerError(c, "failed to update account policy", err)
	}
	return results.SendOKResponse(c, results.NewAccountPolicyResult(policy), "account policy updated successfully")
}

func (h *OrganizationalUnitHandler) authContext(c echo.Context) (commands.AuthContext, error) {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return commands.AuthContext{}, err
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return commands.AuthContext{}, err
	}
	return commands.NewAuthContext(accountID, userID), nil
}

// Create creates an organizational unit.
// @Summary Create organizational unit
// @Description Creates an organizational unit. Omit parent_id for a root unit; set it (to a root unit) to nest one level.
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param ou body organizational_unit_create.OrganizationalUnitCreateCommand true "OU data"
// @Success 201 {object} results.APIResponse{data=results.OrganizationalUnitResult} "Created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units [post]
func (h *OrganizationalUnitHandler) Create(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var cmd organizational_unit_create.OrganizationalUnitCreateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.ID = uuid.New()
	cmd.AuthContext = auth

	ou, err := h.service.Create(c.Request().Context(), cmd)
	if err != nil {
		if errors.Is(err, organizational_unit.ErrNameRequired) || errors.Is(err, organizational_unit.ErrNestingTooDeep) {
			return results.SendValidationError(c, err.Error())
		}
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendValidationError(c, "parent organizational unit not found")
		}
		return results.SendInternalServerError(c, "failed to create organizational unit", err)
	}
	return results.SendCreatedResponse(c, results.NewOrganizationalUnitResult(ou), "Organizational unit created successfully")
}

// List lists organizational units for the account.
// @Summary List organizational units
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.OrganizationalUnitListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units [get]
func (h *OrganizationalUnitHandler) List(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ous, err := h.service.GetAllByAccountID(c.Request().Context(), organizational_unit_list.OrganizationalUnitListQuery{AuthContext: auth})
	if err != nil {
		return results.SendInternalServerError(c, "failed to list organizational units", err)
	}

	// OU-scoped members see only their allowed subtree — plus each allowed
	// unit's parent, read-only context without which the child cannot render
	// its inheritance chain. Account-wide readers see the whole tree.
	allowAll, allowedOUs, err := h.enforcer.AllowedOUs(c.Request().Context(), auth.AccountID, auth.UserID, authz.ActionOURead)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	if !allowAll {
		visible := make(map[uuid.UUID]bool, len(allowedOUs))
		for id := range allowedOUs {
			visible[id] = true
		}
		for i := range ous {
			if allowedOUs[ous[i].ID] && ous[i].ParentID != nil {
				visible[*ous[i].ParentID] = true
			}
		}
		filtered := ous[:0]
		for i := range ous {
			if visible[ous[i].ID] {
				filtered = append(filtered, ous[i])
			}
		}
		ous = filtered
	}
	return results.SendOKResponse(c, results.NewOrganizationalUnitListResult(ous), "Organizational units retrieved successfully")
}

// Get retrieves an organizational unit by ID.
// @Summary Get organizational unit
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=results.OrganizationalUnitResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id} [get]
func (h *OrganizationalUnitHandler) Get(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionOURead, &id); err != nil {
		return err
	}
	ou, err := h.service.GetByID(c.Request().Context(), organizational_unit_get.OrganizationalUnitGetQuery{ID: id, AuthContext: auth})
	if err != nil {
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendNotFoundError(c, "organizational unit not found")
		}
		return results.SendInternalServerError(c, "failed to get organizational unit", err)
	}
	return results.SendOKResponse(c, results.NewOrganizationalUnitResult(ou), "Organizational unit retrieved successfully")
}

// Update updates an organizational unit.
// @Summary Update organizational unit
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Param ou body organizational_unit_update.OrganizationalUnitUpdateCommand true "OU update"
// @Success 200 {object} results.APIResponse{data=results.OrganizationalUnitResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id} [put]
func (h *OrganizationalUnitHandler) Update(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionOUManage, &id); err != nil {
		return err
	}
	var cmd organizational_unit_update.OrganizationalUnitUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.ID = id
	cmd.AuthContext = auth

	// A reparent changes which ROOT the unit's nodes inherit access entries,
	// secrets and policy from (union resolution), and dispatches
	// apply_access_config to them — so the DESTINATION needs the same right as
	// the unit being moved. Authorizing only the moved unit would let an
	// OU-scoped ou.manage holder (a tunable grant) pull an arbitrary root's
	// inherited config onto their nodes. "" (make root) escapes the parent's
	// subtree, which is the account-level question. An unparseable value falls
	// through to the service's own validation.
	if cmd.ParentID != nil {
		if *cmd.ParentID == "" {
			if err := middleware.Authorize(c, h.enforcer, authz.ActionOUManage, nil); err != nil {
				return err
			}
		} else if destID, perr := uuid.Parse(*cmd.ParentID); perr == nil {
			if err := middleware.Authorize(c, h.enforcer, authz.ActionOUManage, &destID); err != nil {
				return err
			}
		}
	}

	ou, err := h.service.Update(c.Request().Context(), cmd)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
			return results.SendNotFoundError(c, "organizational unit not found")
		case errors.Is(err, organizational_unit.ErrNestingTooDeep),
			errors.Is(err, organizational_unit.ErrParentIsNotRoot),
			errors.Is(err, organizational_unit.ErrCircularParent):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to update organizational unit", err)
		}
	}
	return results.SendOKResponse(c, results.NewOrganizationalUnitResult(ou), "Organizational unit updated successfully")
}

// Delete deletes an organizational unit.
// @Summary Delete organizational unit
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 409 {object} results.APIResponse "Still has children, nodes or scoped playbooks"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id} [delete]
func (h *OrganizationalUnitHandler) Delete(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionOUManage, &id); err != nil {
		return err
	}
	err = h.service.Delete(c.Request().Context(), organizational_unit_delete.OrganizationalUnitDeleteCommand{ID: id, AuthContext: auth})
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
			return results.SendNotFoundError(c, "organizational unit not found")
		case errors.Is(err, app_errors.ErrOrganizationalUnitHasChild):
			return results.SendValidationError(c, "organizational unit still has nested units")
		case errors.Is(err, app_errors.ErrOrganizationalUnitHasNodes):
			return results.SendValidationError(c, "organizational unit still has nodes assigned")
		case errors.Is(err, app_errors.ErrOrganizationalUnitHasPlaybooks):
			return results.SendValidationError(c, "organizational unit still has playbooks scoped to it")
		case errors.Is(err, app_errors.ErrOrganizationalUnitHasUtilities):
			return results.SendValidationError(c, "organizational unit still has utilities assigned to it")
		default:
			return results.SendInternalServerError(c, "failed to delete organizational unit", err)
		}
	}
	return results.SendNoContentResponse(c)
}

type assignNodeRequest struct {
	OUID *uuid.UUID `json:"ou_id"` // null to unassign
}

// AssignNode assigns (or clears with null ou_id) a node's organizational unit.
// @Summary Assign node to organizational unit
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param body body assignNodeRequest true "OU assignment (ou_id null to unassign)"
// @Success 200 {object} results.APIResponse{data=map[string]string} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/organizational-unit [put]
func (h *OrganizationalUnitHandler) AssignNode(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	var req assignNodeRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if err := h.service.AssignNode(c.Request().Context(), audit.UserActor(auth.AccountID, auth.UserID), auth.AccountID, nodeID, req.OUID); err != nil {
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendNotFoundError(c, "organizational unit not found")
		}
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to assign node", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "assigned"}, "Node organizational unit updated")
}

type setNodePolicyRequest struct {
	NetworkFirst    *organizational_unit.PolicyFlag `json:"network_first,omitempty"`
	BootToOS        *organizational_unit.PolicyFlag `json:"boot_to_os,omitempty"`
	SSHPasswordAuth *organizational_unit.PolicyFlag `json:"ssh_password_auth,omitempty"`
}

// SetNodePolicy overrides a node's policy flags (inherit|on|off).
// @Summary Set node policy overrides
// @Tags organizational-units
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param body body setNodePolicyRequest true "Policy overrides"
// @Success 200 {object} results.APIResponse{data=map[string]string} "OK"
// @Failure 400 {object} results.APIResponse "A flag outside inherit, on, off (code invalid_policy_flag)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/policy [put]
func (h *OrganizationalUnitHandler) SetNodePolicy(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	var req setNodePolicyRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if err := h.service.SetNodePolicy(c.Request().Context(), audit.UserActor(auth.AccountID, auth.UserID), auth.AccountID, nodeID, req.NetworkFirst, req.BootToOS, req.SSHPasswordAuth); err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to set node policy", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "updated"}, "Node policy updated")
}

// ResetNodePolicy clears every per-node policy override back to inheritance.
// @Summary Reset node policy overrides
// @Description Clears all per-node policy overrides (boot flags, SSH password login, provisioning defaults, hardware platform, LED behaviour) so the node inherits from its unit chain again. Pushes the access config to the node when an SSH password login override was cleared.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]string} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/policy/reset [post]
func (h *OrganizationalUnitHandler) ResetNodePolicy(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if err := h.service.ResetNodePolicyOverrides(c.Request().Context(), auth.AccountID, auth.UserID, nodeID); err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to reset node policy overrides", err)
	}
	return results.SendOKResponse(c, map[string]string{"status": "reset"}, "Node policy overrides cleared")
}

// EffectivePolicy returns the resolved policy for a node.
// @Summary Get node effective policy
// @Description Resolves the effective policy for a node walking node -> OU -> root OU -> default, with the source of each value.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=organizational_unit.EffectivePolicy} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/effective-policy [get]
func (h *OrganizationalUnitHandler) EffectivePolicy(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	policy, err := h.service.ResolveEffectivePolicy(c.Request().Context(), auth.AccountID, nodeID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to resolve effective policy", err)
	}
	return results.SendOKResponse(c, policy, "Effective policy resolved")
}

// NodeEffectiveSecrets returns a node's resolved secret set with provenance.
// @Summary Get node effective secrets
// @Description Resolves the node's effective secret set (direct + OU/root + group attachments, node > OU > root precedence) with the source of each entry. Values are included only for non-sensitive entries.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.EffectiveSecretListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/effective-secrets [get]
func (h *OrganizationalUnitHandler) NodeEffectiveSecrets(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	secrets, err := h.service.ResolveEffectiveSecrets(c.Request().Context(), auth.AccountID, nodeID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNodeNotFound) {
			return results.SendNotFoundError(c, "node not found")
		}
		return results.SendInternalServerError(c, "failed to resolve effective secrets", err)
	}
	effResult := results.NewEffectiveSecretListResult(secrets)
	if !h.canReadSecretValues(c, audit.ScopeNodeSecrets, audit_event.TargetNode, c.Param("id")) {
		effResult.RedactValues()
	}
	return results.SendOKResponse(c, effResult, "Effective secrets resolved")
}

// Summary returns the Organization page's one-call overview of a unit.
// @Summary Get organizational unit summary
// @Description Returns the unit with its effective (inherited) policy, counts, and first-N samples of the nodes, access entries, secrets, playbooks and adoption tokens it delivers. Sections the caller's role cannot read are absent and named in omitted.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=results.OrganizationalUnitSummaryResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/summary [get]
func (h *OrganizationalUnitHandler) Summary(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	// The unit view itself is ou.read against the unit; each section is gated
	// by ITS resource's read action against the same target, so the summary
	// never shows a caller more than the original pages would.
	if err := middleware.Authorize(c, h.enforcer, authz.ActionOURead, &id); err != nil {
		return err
	}
	can := func(action authz.Action) bool {
		return middleware.Authorize(c, h.enforcer, action, &id) == nil
	}
	include := ou_service.SummarySections{
		Nodes: can(authz.ActionNodeRead),
		// PARITY with the access-entries API, which requires ACCOUNT-WIDE
		// access.manage: the section lists parent-level and account-wide
		// entries (that is its provenance value), so a per-unit grant must not
		// open here what GET /access-entries refuses — otherwise an OU-scoped
		// admin would read the account's OS-username map through this section.
		Access:    middleware.Authorize(c, h.enforcer, authz.ActionAccessManage, nil) == nil,
		Secrets:   can(authz.ActionSecretRead),
		Workflows: can(authz.ActionPlaybookRead),
		// Master tokens are an account-global resource; their manage action is
		// super_admin's and is asked at the account level, not the unit's.
		MasterTokens: middleware.Authorize(c, h.enforcer, authz.ActionMasterTokenManage, nil) == nil,
	}

	summary, err := h.service.Summary(c.Request().Context(),
		organizational_unit_summary.OrganizationalUnitSummaryQuery{ID: id, AuthContext: auth}, include)
	if err != nil {
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendNotFoundError(c, "organizational unit not found")
		}
		return results.SendInternalServerError(c, "failed to build organizational unit summary", err)
	}
	return results.SendOKResponse(c, summary, "Organizational unit summary retrieved successfully")
}

// OUEffectiveSecrets returns an OU's resolved secret set with provenance.
// @Summary Get organizational unit effective secrets
// @Description Resolves the secret set visible at an OU (its own attachments plus those inherited from its root unit, groups expanded) with the source of each entry. Values are included only for non-sensitive entries.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=results.EffectiveSecretListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/effective-secrets [get]
func (h *OrganizationalUnitHandler) OUEffectiveSecrets(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ouID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionSecretRead, &ouID); err != nil {
		return err
	}
	secrets, err := h.service.ResolveEffectiveSecretsForOU(c.Request().Context(), auth.AccountID, ouID)
	if err != nil {
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendNotFoundError(c, "organizational unit not found")
		}
		return results.SendInternalServerError(c, "failed to resolve effective secrets", err)
	}
	ouEffResult := results.NewEffectiveSecretListResult(secrets)
	if !h.canReadSecretValues(c, audit.ScopeOUSecrets, audit_event.TargetOU, c.Param("id")) {
		ouEffResult.RedactValues()
	}
	return results.SendOKResponse(c, ouEffResult, "Effective secrets resolved")
}

// ApplyDeviceProfileToUnit dispatches the per-node device-profile apply to
// every node directly assigned to the unit.
// @Summary Apply the effective device profile to all of a unit's nodes
// @Description Runs the per-node apply (apply_device_profile) over every node DIRECTLY assigned to this unit. Per-node refusals do not abort the sweep: an unfitting board, a watcher node or a node with nothing assigned lands in skipped with the platform's refusal wording, and the rest of the unit still dispatches. Offline nodes queue. Nothing ever dispatches on an equipage EDIT — this button is the only fleet path.
// @Tags organizational-units
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=services.UnitDeviceProfileApplyReport} "dispatched / queued counts and per-node skips"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Organizational unit not found"
// @Router /api/v1/organizational-units/{id}/device-profile/apply [post]
func (h *OrganizationalUnitHandler) ApplyDeviceProfileToUnit(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	// The same action the per-node apply requires, asked against the unit.
	if err := middleware.Authorize(c, h.enforcer, authz.ActionNodeFileWrite, &id); err != nil {
		return err
	}
	// Existence first, so a bogus id is a 404 rather than a 200 with zeros.
	if _, err := h.service.GetByID(c.Request().Context(), organizational_unit_get.OrganizationalUnitGetQuery{ID: id, AuthContext: auth}); err != nil {
		if errors.Is(err, app_errors.ErrOrganizationalUnitNotFound) {
			return results.SendNotFoundError(c, "organizational unit not found")
		}
		return results.SendInternalServerError(c, "failed to load organizational unit", err)
	}
	if h.nodeService == nil {
		return results.SendInternalServerError(c, "node service unavailable", nil)
	}
	report, err := h.nodeService.ApplyDeviceProfileToUnit(c.Request().Context(), auth.AccountID, userID, id)
	if err != nil {
		return results.SendInternalServerError(c, "failed to apply the device profile to the unit", err)
	}
	return results.SendOKResponse(c, report, "device profile apply dispatched to the unit")
}
