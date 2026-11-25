package account

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// AccountHandler handles account-related HTTP requests
type AccountHandler struct {
	accountService services.AccountService
	// auditRetentionDays is the install's default audit retention, so the
	// account record can say how long its own trail is kept.
	auditRetentionDays int
	logger             *slog.Logger
}

// NewAccountHandler creates a new account handler. auditRetentionDays is the
// install's default audit retention (AUDIT_EVENT_RETENTION_DAYS).
func NewAccountHandler(accountService services.AccountService, auditRetentionDays int, logger *slog.Logger) *AccountHandler {
	return &AccountHandler{
		accountService:     accountService,
		auditRetentionDays: auditRetentionDays,
		logger:             logger.With("handler", "account"),
	}
}

// RegisterRoutes registers account routes
func (h *AccountHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	accounts := e.Group("/api/v1/accounts")
	accounts.Use(authMiddleware)

	// POST /accounts is retired: account creation is signup-only (it would
	// otherwise mint an account the creator's pinned token cannot use).
	// Any active member may read their own account record; the handler's
	// id-matches-token check alone would let a REMOVED member keep reading it
	// until the access token expired.
	accounts.GET("/:id", h.GetAccount, middleware.RequireActiveMember(enforcer))
	accounts.PUT("/:id", h.UpdateAccount, middleware.RequireAction(enforcer, authz.ActionAccountUpdate))
	// DELETE answers 403 for every caller (see DeleteAccount), so it neither
	// deletes the instance operator's account nor needs a step-up. Whoever
	// wires it back to AccountService.DeleteAccount inherits that service's
	// operator refusal (409 instance_operator_account); step-up is not added
	// because the doctrine gates routes that ADD or REPOINT a way to sign in,
	// and a delete is destructive but does neither.
	accounts.DELETE("/:id", h.DeleteAccount, middleware.RequireAction(enforcer, authz.ActionAccountDelete))
}

// An account is created only by the sign-up flow, which mints it alongside its
// first user and its owner binding. The standalone CreateAccount handler that
// used to sit here was routed nowhere while still carrying `@Router
// /api/v1/accounts [post]`, so the published API advertised an endpoint that
// answered 404 — the worst of both, since a client could believe accounts are
// creatable and an auditor could believe the route is a real surface to review.

// GetAccount retrieves an account by ID
// @Summary Get account by ID
// @Description Retrieves account details by ID
// @Tags accounts
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Account ID"
// @Success 200 {object} results.APIResponse{data=results.AccountResult} "Account retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Account not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/accounts/{id} [get]
func (h *AccountHandler) GetAccount(c echo.Context) error {
	// Get user's account ID from context
	userAccountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		h.logger.Warn("invalid account ID format", "error", err)
		return results.SendValidationError(c, "invalid account ID")
	}

	// For now, we'll keep the basic check here since GetAccount doesn't use commands
	// In a full implementation, this would be moved to a query with auth context
	if userAccountID != id {
		h.logger.Error("unauthorized account access", "user_account_id", userAccountID, "requested_account_id", id)
		return results.SendForbiddenError(c, "access denied")
	}

	acc, err := h.accountService.GetAccount(c.Request().Context(), id)
	if err != nil {
		h.logger.Error("failed to get account", "error", err, "account_id", id)
		if errors.Is(err, app_errors.ErrAccountNotFound) {
			return results.SendNotFoundError(c, "account not found")
		}
		return results.SendInternalServerError(c, "failed to get account", err)
	}

	accountResult := results.NewAccountResult(acc, h.auditRetentionDays)
	return results.SendOKResponse(c, accountResult, "account retrieved successfully")
}

// UpdateAccount updates an account
// @Summary Update account
// @Description Updates account information
// @Tags accounts
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Account ID"
// @Param account body account_update.AccountUpdateCommand true "Account update data"
// @Success 200 {object} results.APIResponse{data=results.AccountResult} "Account updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Cannot update another account"
// @Failure 404 {object} results.APIResponse "Account not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/accounts/{id} [put]
func (h *AccountHandler) UpdateAccount(c echo.Context) error {
	// Get user's account ID and user ID from context
	userAccountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		h.logger.Warn("invalid account ID format", "error", err)
		return results.SendValidationError(c, "invalid account ID")
	}

	var cmd account_update.AccountUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context in command
	cmd.ID = id
	cmd.AuthContext = commands.NewAuthContext(userAccountID, userID)

	if err := cmd.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	updatedAccount, err := h.accountService.UpdateAccount(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update account", err, "account_id", id)
		if errors.Is(err, app_errors.ErrAccountNotFound) {
			return results.SendNotFoundError(c, "account not found")
		}
		if errors.Is(err, app_errors.ErrUnauthorized) {
			return results.SendForbiddenError(c, "cannot update another account")
		}
		return results.SendInternalServerError(c, "failed to update account", err)
	}

	accountResult := results.NewAccountResult(updatedAccount, h.auditRetentionDays)
	return results.SendOKResponse(c, accountResult, "account updated successfully")
}

// DeleteAccount is DISABLED: the previous implementation deleted only the
// RBAC rows (bindings/invites/role settings/members) and the accounts row,
// orphaning every other account-owned table (nodes, secrets, images incl.
// their files on disk, workflows, OUs, tokens) and leaking storage. A
// complete, audited purge is carried out by the operator; tenant
// self-serve deletion returns if/when a shared purge implementation exists.
// @Summary Delete account (disabled)
// @Description Disabled: self-serve account deletion previously orphaned account data. Contact the operator to delete an account.
// @Tags accounts
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Account ID"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Self-serve account deletion is disabled"
// @Router /api/v1/accounts/{id} [delete]
func (h *AccountHandler) DeleteAccount(c echo.Context) error {
	// Auth context is still resolved so the attempt is attributable in logs.
	_, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	h.logger.Warn("self-serve account deletion attempt (disabled endpoint)",
		"account_id", c.Param("id"), "user_id", userID)
	return results.SendForbiddenError(c,
		"self-serve account deletion is disabled — contact the operator to delete an account")
}
