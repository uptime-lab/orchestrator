package authz

import (
	"net/http"

	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/labstack/echo/v4"
)

// AuthzHandler serves the computed permission map (for the frontend) and
// per-account role-permission settings (super_admin only).
type AuthzHandler struct {
	enforcer *authz_service.Enforcer
	logger   *slog.Logger
}

func NewAuthzHandler(enforcer *authz_service.Enforcer, logger *slog.Logger) *AuthzHandler {
	return &AuthzHandler{enforcer: enforcer, logger: logger.With("handler", "authz")}
}

func (h *AuthzHandler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc) {
	users := e.Group("/api/v1/users")
	users.Use(authMiddleware)
	users.GET("/me/permissions", h.MyPermissions)

	settings := e.Group("/api/v1/account-role-settings")
	settings.Use(authMiddleware)
	// Where the account has loosened its defaults on the tunable actions is
	// tenant data, and this route's own Swagger text has always claimed "any
	// active member may read" — nothing enforced it. Auth alone does not: the
	// JWT middleware never resolves membership.
	settings.GET("", h.ListRoleSettings, middleware.RequireActiveMember(h.enforcer))
	// Editing role settings is itself a super_admin action (member.manage), and
	// it re-grants privileges account-wide — the same class of change as
	// rewriting a member's bindings, so it takes the same step-up.
	manage := middleware.RequireAction(h.enforcer, authz.ActionMemberManage)
	settings.PUT("", h.UpsertRoleSetting, manage, recentAuth)
	settings.DELETE("", h.ResetRoleSetting, manage, recentAuth)
}

// MyPermissions returns the caller's computed permission map
// @Summary Get my permissions
// @Description Returns a map of action -> scope ("*" for account-wide, otherwise a list of OU ids) for the current user in the current account. Drives UI gating; the backend remains the source of truth.
// @Tags authz
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Permission map"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/users/me/permissions [get]
func (h *AuthzHandler) MyPermissions(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	perms, err := h.enforcer.PermissionMap(c.Request().Context(), accountID, userID)
	if err != nil {
		return middleware.MembershipHTTPError(err)
	}
	return results.SendOKResponse(c, perms, "permissions retrieved successfully")
}

// ListRoleSettings returns the per-role tunable-permission view
// @Summary List role permissions
// @Description Returns, per tunable role, every tunable action with its default value, effective value and whether an override exists. Any active member may read; only super_admin may change.
// @Tags authz
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]authz_service.RolePermissions} "Role permissions"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/account-role-settings [get]
func (h *AuthzHandler) ListRoleSettings(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	view, err := h.enforcer.RolePermissionsView(c.Request().Context(), accountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list role permissions", err)
	}
	return results.SendOKResponse(c, view, "role permissions retrieved successfully")
}

type upsertRoleSettingRequest struct {
	Role    string `json:"role"`
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
}

// UpsertRoleSetting overrides a tunable role permission for the account
// @Summary Set a role-permission override
// @Description Overrides a tunable action for a role in this account (super_admin only). Only whitelisted actions and non-super_admin roles are accepted.
// @Tags authz
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param setting body upsertRoleSettingRequest true "Role, action, allowed"
// @Success 200 {object} results.APIResponse "Updated"
// @Failure 400 {object} results.APIResponse "Not a tunable action/role"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Router /api/v1/account-role-settings [put]
func (h *AuthzHandler) UpsertRoleSetting(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req upsertRoleSettingRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	err = h.enforcer.UpsertRoleSetting(c.Request().Context(), accountID, userID,
		authz.Role(req.Role), authz.Action(req.Action), req.Allowed)
	if err != nil {
		return results.SendError(c, http.StatusBadRequest, err.Error(), err)
	}
	return results.SendOKResponse(c, nil, "role setting updated successfully")
}

// ResetRoleSetting reverts overrides to the built-in default
// @Summary Reset role-permission overrides
// @Description Reverts a role's tunable permissions to the built-in defaults. With `action` set, resets that one permission; without it, resets the whole role. super_admin only.
// @Tags authz
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param role query string true "Role to reset"
// @Param action query string false "Single action to reset (omit to reset the whole role)"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Invalid role/action"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Router /api/v1/account-role-settings [delete]
func (h *AuthzHandler) ResetRoleSetting(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	role := authz.Role(c.QueryParam("role"))
	action := c.QueryParam("action")
	if role == "" {
		return results.SendValidationError(c, "role is required")
	}
	if action != "" {
		err = h.enforcer.ResetRoleSetting(c.Request().Context(), accountID, actorUserID, role, authz.Action(action))
	} else {
		err = h.enforcer.ResetRole(c.Request().Context(), accountID, actorUserID, role)
	}
	if err != nil {
		return results.SendError(c, http.StatusBadRequest, err.Error(), err)
	}
	return c.NoContent(http.StatusNoContent)
}
