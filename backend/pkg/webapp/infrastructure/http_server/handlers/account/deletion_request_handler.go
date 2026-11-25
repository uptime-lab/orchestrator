package account

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_deletion_request"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// DeletionRequestHandler serves a tenant's standing request to be erased.
//
// It files a request; it does not delete anything. The erasure is the admin
// console's, which owns the 41-table inventory, the tripwire and the
// least-privilege role — see the domain package for why duplicating that here
// would be the worse choice.
type DeletionRequestHandler struct {
	repo     repository.AccountDeletionRequestRepository
	accounts services.AccountService
	audit    *audit_service.Recorder
	logger   *slog.Logger
	// installKind says whether an operator console executes the request (the
	// hosted install). Unwired, the notice is the self-hosted one, which
	// promises less.
	installKind InstallKindReporter
}

// InstallKindReporter tells the hosted install, whose operator console
// performs an erasure, from a self-hosted one, which has nothing that does.
type InstallKindReporter interface {
	IsHostedInstall(ctx context.Context) bool
}

// SetInstallKind wires the reporter the notice is chosen by.
func (h *DeletionRequestHandler) SetInstallKind(k InstallKindReporter) {
	if k == nil {
		panic("account deletion request handler: install kind reporter is nil")
	}
	h.installKind = k
}

// erasureExecutor reports whether this install performs the erasure.
func (h *DeletionRequestHandler) erasureExecutor(c echo.Context) bool {
	return h.installKind != nil && h.installKind.IsHostedInstall(c.Request().Context())
}

func NewDeletionRequestHandler(repo repository.AccountDeletionRequestRepository, accounts services.AccountService, auditRecorder *audit_service.Recorder, logger *slog.Logger) *DeletionRequestHandler {
	if repo == nil {
		panic("account deletion request handler requires a repository")
	}
	if accounts == nil {
		panic("account deletion request handler requires the account service")
	}
	if auditRecorder == nil {
		// The row is transient — cancelling deletes it — so the audit trail is
		// the ONLY durable record that a customer ever asked to be erased. A
		// handler that could file one without recording it is the thing this
		// panic exists to prevent.
		panic("account deletion request handler requires an audit recorder")
	}
	return &DeletionRequestHandler{repo: repo, accounts: accounts, audit: auditRecorder,
		logger: logger.With("handler", "account_deletion_request")}
}

// RegisterRoutes wires the three routes onto the caller's OWN account.
//
// Singular /account and no id, like the export beside it: the request is always
// about the account in the token, so tenancy is a property rather than a check.
func (h *DeletionRequestHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	g := e.Group("/api/v1/account/deletion-request")
	g.Use(authMiddleware)
	del := middleware.RequireAction(enforcer, authz.ActionAccountDelete)
	g.POST("", h.Request, del)
	g.GET("", h.Get, del)
	g.DELETE("", h.Cancel, del)
}

// DeletionRequestBody is the request payload.
type DeletionRequestBody struct {
	// AccountName must match the account's real name. It is the control, not
	// the dialog: the same reasoning as any irreversible deletion, where the
	// type-the-name box is UX and the server-side echo is what stops a
	// misdirected click.
	AccountName string `json:"account_name"`
	Reason      string `json:"reason,omitempty"`
}

// Request files a deletion request for the caller's account
// @Summary Request deletion of this account
// @Description Files a standing request to have the account and all its data erased. It does NOT delete anything: on the hosted install the instance operator performs the erasure with tooling that can do it completely; a self-hosted install records the request for its operator and erases nothing on its own. The response's notice says which. Requires typing the account name exactly, and account.delete (super_admin). Cancel it with DELETE while it is open.
// @Tags accounts
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body DeletionRequestBody true "Confirmation"
// @Success 201 {object} results.APIResponse{data=results.AccountDeletionRequestResult} "Request filed"
// @Failure 400 {object} results.APIResponse "Account name does not match"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 409 {object} results.APIResponse "A request is already open"
// @Router /api/v1/account/deletion-request [post]
func (h *DeletionRequestHandler) Request(c echo.Context) error {
	accountID, userID, err := h.actor(c)
	if err != nil {
		return err
	}
	var body DeletionRequestBody
	if err := c.Bind(&body); err != nil {
		return results.SendValidationError(c, "invalid request format")
	}

	ctx := c.Request().Context()
	acct, err := h.accounts.GetAccount(ctx, accountID)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to read the account", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to read the account", err)
	}

	req, err := account_deletion_request.New(accountID, userID, acct.Name, body.AccountName, body.Reason)
	if err != nil {
		if errors.Is(err, account_deletion_request.ErrNameMismatch) {
			// 400, and the message names what was expected of the CALLER without
			// echoing the account name back — the caller already knows it, and a
			// mismatch is a confirmation failure, not a lookup.
			return results.SendValidationError(c, "the account name does not match; type it exactly as it appears in your settings")
		}
		return results.SendValidationError(c, err.Error())
	}

	if err := h.repo.Create(ctx, req); err != nil {
		if errors.Is(err, account_deletion_request.ErrAlreadyRequested) {
			return results.SendError(c, http.StatusConflict, "a deletion request is already open for this account", err)
		}
		h.logger.ErrorContext(ctx, "failed to file the deletion request", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to file the request", err)
	}

	// The reason is NOT copied into the audit detail. It is free text a customer
	// wrote, so it can contain anything — including a credential they pasted by
	// mistake — and audit_events is on no rewrap list, which is exactly where
	// such a thing must not land. The row itself carries it, and the row is
	// deletable.
	h.audit.Record(ctx, audit_service.UserActor(accountID, userID),
		audit_event.ActionAccountDeletionRequested, "account", accountID.String())
	h.logger.WarnContext(ctx, "ACCOUNT DELETION REQUESTED by the tenant; operator action needed",
		"account_id", accountID, "account_name", acct.Name, "requested_by", userID)

	return results.SendCreatedResponse(c, results.NewAccountDeletionRequestResult(req, h.erasureExecutor(c)), "deletion request filed")
}

// Get returns the open deletion request, if any
// @Summary Get this account's open deletion request
// @Tags accounts
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.AccountDeletionRequestResult} "The open request"
// @Failure 404 {object} results.APIResponse "No request is open"
// @Router /api/v1/account/deletion-request [get]
func (h *DeletionRequestHandler) Get(c echo.Context) error {
	accountID, _, err := h.actor(c)
	if err != nil {
		return err
	}
	req, err := h.repo.GetByAccountID(c.Request().Context(), accountID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNotFound) {
			return results.SendNotFoundError(c, "no deletion request is open for this account")
		}
		return results.SendInternalServerError(c, "failed to read the request", err)
	}
	return results.SendOKResponse(c, results.NewAccountDeletionRequestResult(req, h.erasureExecutor(c)), "deletion request")
}

// Cancel withdraws the open deletion request
// @Summary Cancel this account's deletion request
// @Description Withdraws a filed request. Possible only while the operator has not yet acted on it — once the account is erased there is nothing left to cancel with.
// @Tags accounts
// @Produce json
// @Security ApiKeyAuth
// @Success 204 "No Content"
// @Failure 404 {object} results.APIResponse "No request is open"
// @Router /api/v1/account/deletion-request [delete]
func (h *DeletionRequestHandler) Cancel(c echo.Context) error {
	accountID, userID, err := h.actor(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	removed, err := h.repo.DeleteByAccountID(ctx, accountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to cancel the request", err)
	}
	if !removed {
		// 404 rather than a cheerful 200: "cancelled" for something that was
		// never open would let a customer believe they had withdrawn a request
		// that some other path had already consumed.
		return results.SendNotFoundError(c, "no deletion request is open for this account")
	}
	h.audit.Record(ctx, audit_service.UserActor(accountID, userID),
		audit_event.ActionAccountDeletionCancelled, "account", accountID.String())
	return c.NoContent(http.StatusNoContent)
}

// actor resolves the caller. Returning the echo error directly keeps the three
// handlers above free of the same six lines.
func (h *DeletionRequestHandler) actor(c echo.Context) (uuid.UUID, uuid.UUID, error) {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, results.SendUnauthorizedError(c, "unauthorized")
	}
	return accountID, userID, nil
}
