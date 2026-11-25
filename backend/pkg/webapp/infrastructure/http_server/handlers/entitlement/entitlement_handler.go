// Package entitlement serves the install's device entitlement: what it runs
// under, and the paste that applies a new file. Both routes sit behind
// entitlement.manage, which no role below super_admin holds, and both
// answer an INSTANCE question: on an install that hosts several accounts
// only the account that operates it (the oldest) may apply a file or read
// its terms; every other account's super_admin reads the summary.
package entitlement

import (
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/entitlement_apply"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Handler serves GET and PUT /api/v1/server/entitlement.
type Handler struct {
	service *entitlement_service.Service
	logger  *slog.Logger
}

// NewHandler wires the handler. The service is required: a route that
// answers "free" because nothing is behind it would be a card that lies.
func NewHandler(service *entitlement_service.Service, logger *slog.Logger) *Handler {
	if service == nil {
		panic("entitlement handler requires the entitlement service")
	}
	return &Handler{service: service, logger: logger.With("handler", "entitlement")}
}

// RegisterRoutes wires the two routes. The PUT changes what the install
// will admit, which is a security-relevant change to the instance, so it
// sits behind the step-up like every other route that changes what an
// account can do; the GET does not, because reading the card is not one.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	g := e.Group("/api/v1/server/entitlement")
	g.Use(authMiddleware)
	manage := middleware.RequireAction(enforcer, authz.ActionEntitlementManage)
	g.GET("", h.Get, manage)
	g.PUT("", h.Apply, manage, recentAuth)
}

// Get returns the install's device entitlement
// @Summary Get the device entitlement
// @Description Returns what this install runs under. Instance-wide. Requires entitlement.manage (super_admin). For the account that operates the install (the oldest account; instance_operator=true): the free device cap, the file in force (customer, kind, cap, issue and expiry, grace, note, source), the effective cap admission enforces right now (never below the free cap: a signed positive cap under it is enforced as the free cap and flagged device_cap_below_free_floor), the install-wide admitted device count (admitted_devices, every account counted), the caller's own account's admitted count (account_devices), and any offered file that did not verify. For any other account: the summary only — state, the caps, the caller's own account's admitted count (account_devices), expiry and grace — never the customer, kind, note, source, rejections or the install-wide count (admitted_devices is absent). A count that could not be read is -1. Both counts are ADMITTED devices, what the cap counts: an approved node past its first connect. A node pending approval, a placeholder or node awaiting its first connect, and a rejected node are not counted, so account_devices can be lower than the number of rows on the Nodes page. mode is instance for the shapes above. When the install sets a per-account device limit (ACCOUNT_DEVICE_LIMIT) and the caller's account is not the instance operator, mode is account_limit and the answer is the account's own: account_device_limit (its limit now; 0 = no limit, -1 = could not be read), account_plan (free = the install default, subscription = an operator's override in force), account_plan_until (when a dated subscription ends; absent otherwise), account_devices and account_limit_contact (where to write for more devices) — and none of the install's entitlement (no state, caps, expiry, grace or subscription_url).
// @Tags server
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.EntitlementResult} "The entitlement in force"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Router /api/v1/server/entitlement [get]
func (h *Handler) Get(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ctx := c.Request().Context()
	snap := h.service.CurrentFor(ctx, accountID)
	operator := h.service.IsInstanceOperator(ctx, accountID)
	return results.SendOKResponse(c, results.NewEntitlementResult(snap, operator), "entitlement retrieved")
}

// Apply applies a signed entitlement file to the install
// @Summary Apply an entitlement file
// @Description Verifies a pasted entitlement file against the keys built into this server and stores it. The newest valid file wins, so pasting the next file extends or upgrades the entitlement and revokes nothing. The answer carries the terms of the file in force and, in applied, the pasted file's own terms and standing: in_force is false when a valid file was stored but another one is in force (it was issued before a file still active or in grace, or it has already lapsed), and state is the pasted file's own phase (active, grace or lapsed). applied appears on this answer only, never on the GET. A file that does not verify is refused with 400 (code entitlement_rejected) and nothing is stored. Requires entitlement.manage (super_admin), a recent step-up confirmation, and that the caller's account operates the install (the oldest account): any other account is refused with 403 (code not_instance_operator).
// @Tags server
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param body body entitlement_apply.EntitlementApplyCommand true "The paste form of the entitlement file"
// @Success 200 {object} results.APIResponse{data=results.EntitlementResult} "The entitlement now in force, and the pasted file in applied"
// @Failure 400 {object} results.APIResponse "Validation error, or entitlement_rejected (the file does not verify or its terms are invalid)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden, reauth_required (confirm your identity and retry), or not_instance_operator (the caller's account does not operate this install)"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/server/entitlement [put]
func (h *Handler) Apply(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var cmd entitlement_apply.EntitlementApplyCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)
	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}
	snap, err := h.service.Apply(c.Request().Context(), cmd)
	if err != nil {
		if !entitlement_service.IsRefusal(err) {
			h.logger.ErrorContext(c.Request().Context(), "failed to apply entitlement", "error", err, "account_id", accountID)
		}
		// A rejection (400) and the not-the-operator refusal (403) are typed
		// and answer as themselves here.
		return results.SendInternalServerError(c, "failed to apply the entitlement", err)
	}
	// Only the instance operator reaches a successful apply.
	return results.SendOKResponse(c, results.NewEntitlementResult(snap, true), "entitlement applied")
}
