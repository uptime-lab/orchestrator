// Package reauth serves step-up confirmation: the user proves who they are
// again, and gets a short-lived ticket that unlocks credential changes.
package reauth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	passkey_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/passkey"
	reauth_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/reauth"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/labstack/echo/v4"
)

// FailedAuthRecorder is the one thing this handler needs from the auth service:
// somewhere to report a rejected proof. Narrow on purpose, and declared here
// rather than imported so the step-up path does not acquire the whole login
// surface just to count a failure.
type FailedAuthRecorder interface {
	NoteFailedLogin(ctx context.Context, sourceIP string)
}

// Handler serves the confirmation endpoints for the signed-in user.
type Handler struct {
	service *reauth_service.Service
	// passkeys runs the WebAuthn assertion when the user confirms with a
	// security key rather than a password or a code.
	passkeys *passkey_service.Service
	// failures reports rejected proofs to the burst detector. These endpoints
	// are authenticated, so a failure here is a narrower and more interesting
	// signal than a failed login: someone holding a live access token is
	// grinding the step-up that guards adding a credential.
	failures FailedAuthRecorder
	logger   *slog.Logger
}

func NewHandler(service *reauth_service.Service, passkeys *passkey_service.Service, failures FailedAuthRecorder, logger *slog.Logger) *Handler {
	return &Handler{service: service, passkeys: passkeys, failures: failures, logger: logger.With("handler", "reauth")}
}

func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	g := e.Group("/api/v1/users/me/reauth")
	g.Use(authMiddleware)
	g.GET("/methods", h.Methods)
	g.POST("/password", h.ConfirmPassword)
	g.POST("/totp", h.ConfirmTOTP)
	g.POST("/passkey/begin", h.BeginPasskey)
	g.POST("/passkey/finish", h.FinishPasskey)
}

type passwordRequest struct {
	Password string `json:"password"`
}

type codeRequest struct {
	Code string `json:"code"`
}

// Methods reports what the user can confirm with.
// @Summary Available confirmation methods
// @Description Reports which credentials the signed-in user can confirm their identity with. `oidc` means they can be sent back to their identity provider, which for a federated user with no local credential is the only proof available. A user with none of these is not asked to confirm at all — nothing could authenticate as them, and they are the ones adding their first authenticator.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Available methods"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/users/me/reauth/methods [get]
func (h *Handler) Methods(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	methods, err := h.service.AvailableMethods(c.Request().Context(), userID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to read confirmation methods", err)
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"password": methods.Password,
		"totp":     methods.TOTP,
		"passkey":  methods.Passkey,
		"oidc":     methods.OIDC,
		"required": methods.Any(),
	}, "confirmation methods retrieved")
}

// ConfirmPassword issues a ticket against the account password.
// @Summary Confirm identity with a password
// @Description Verifies the current password and returns a short-lived ticket that unlocks credential changes. Send it back in the X-Reauth-Ticket header. The ticket is bound to the session that earned it and is honoured only while that session is live: a sign-out, a "sign out everywhere" or a password change ends it, and a dead session answers 401.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body passwordRequest true "Current password"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Confirmation ticket"
// @Failure 400 {object} results.APIResponse "That did not match"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 429 {object} results.APIResponse "Too many attempts"
// @Router /api/v1/users/me/reauth/password [post]
func (h *Handler) ConfirmPassword(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req passwordRequest
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return results.SendValidationError(c, "password is required")
	}
	ticket, err := h.service.ConfirmPassword(c.Request().Context(), userID, req.Password, middleware.GetSessionProofFromContext(c))
	return h.respond(c, ticket, err)
}

// ConfirmTOTP issues a ticket against the second factor.
// @Summary Confirm identity with a second-factor code
// @Description Verifies a current TOTP code (a single-use recovery code also works) and returns a short-lived ticket. Send it back in the X-Reauth-Ticket header. Bound to the calling session exactly as the password confirmation is.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body codeRequest true "Code from the authenticator"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Confirmation ticket"
// @Failure 400 {object} results.APIResponse "That did not match"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 429 {object} results.APIResponse "Too many attempts"
// @Router /api/v1/users/me/reauth/totp [post]
func (h *Handler) ConfirmTOTP(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req codeRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return results.SendValidationError(c, "code is required")
	}
	ticket, err := h.service.ConfirmTOTP(c.Request().Context(), userID, req.Code, middleware.GetSessionProofFromContext(c))
	return h.respond(c, ticket, err)
}

// BeginPasskey starts a WebAuthn assertion for confirmation.
// @Summary Start passkey confirmation
// @Description Begins a WebAuthn assertion used to confirm identity rather than to sign in.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Assertion options and ticket"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/users/me/reauth/passkey/begin [post]
func (h *Handler) BeginPasskey(c echo.Context) error {
	if _, err := middleware.GetUserIDFromContext(c); err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	assertion, ticket, err := h.passkeys.BeginLogin(c.Request())
	if err != nil {
		return results.SendInternalServerError(c, "failed to start passkey confirmation", err)
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"ticket":  ticket,
		"options": assertion.Response,
	}, "passkey confirmation started")
}

// FinishPasskey verifies the assertion and issues a ticket.
// @Summary Finish passkey confirmation
// @Description Verifies the authenticator's assertion and returns a short-lived confirmation ticket. The asserted credential must belong to the signed-in user.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Confirmation ticket"
// @Failure 400 {object} results.APIResponse "Confirmation failed"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 410 {object} results.APIResponse "The request expired"
// @Router /api/v1/users/me/reauth/passkey/finish [post]
func (h *Handler) FinishPasskey(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	// Query param, not body: the body must stay the verbatim WebAuthn assertion
	// JSON the library parses — the same constraint the registration ceremony has.
	ticket := c.QueryParam("ticket")
	if ticket == "" {
		return results.SendValidationError(c, "ticket is required")
	}
	asserted, err := h.passkeys.FinishLogin(c.Request().Context(), c.Request(), ticket)
	switch {
	case err == nil:
	case errors.Is(err, app_errors.ErrPasskeyCeremonyExpired):
		return results.SendError(c, http.StatusGone, "this request expired; start again", nil)
	default:
		return results.SendValidationError(c, "the authenticator's response could not be verified")
	}
	// The assertion proves SOMEBODY's credential, not necessarily this session's
	// owner. Without this check a passkey belonging to any other user would
	// unlock credential changes on the account whose session is open.
	if asserted.ID != userID {
		h.logger.WarnContext(c.Request().Context(), "passkey confirmation asserted a different user",
			"session_user_id", userID, "asserted_user_id", asserted.ID)
		return results.SendValidationError(c, "that passkey does not belong to this account")
	}
	confirmation, err := h.service.Grant(c.Request().Context(), userID, middleware.GetSessionProofFromContext(c))
	return h.respond(c, confirmation, err)
}

// respond maps a confirmation outcome onto the wire, keeping the failure cases
// identical across methods so a response never reveals which credential exists.
// noteFailure reports one rejected proof. Nil-tolerant so a caller that has not
// wired the recorder loses the signal rather than the endpoint.
func (h *Handler) noteFailure(c echo.Context) {
	if h.failures == nil {
		return
	}
	h.failures.NoteFailedLogin(c.Request().Context(), c.RealIP())
}

func (h *Handler) respond(c echo.Context, ticket string, err error) error {
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]interface{}{
			"ticket":     ticket,
			"expires_in": int(h.service.TTL().Seconds()),
		}, "identity confirmed")
	case errors.Is(err, app_errors.ErrTokenInvalid), errors.Is(err, app_errors.ErrUserNotFound):
		// The session behind the access token is no longer live (signed out,
		// revoked, or older than the last password change): no ticket is
		// issued to it, and the SPA should end it rather than keep prompting
		// Not a failed proof, so not counted as one.
		return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
	case errors.Is(err, app_errors.ErrTwoFactorCodeAlreadyUsed):
		// Not noteFailure: the code was RIGHT, it was simply already spent
		// inside its window. Counting it would let a double-submitted form
		// trip the burst detector on the user's own valid code.
		return results.SendValidationError(c, app_errors.ErrTwoFactorCodeAlreadyUsed.Error())
	case errors.Is(err, app_errors.ErrReauthInvalid),
		errors.Is(err, app_errors.ErrTwoFactorInvalidCode):
		// One call site covers all four confirm endpoints, because they all
		// answer through here. The per-endpoint limiter below already slows a
		// grinder down; this is what makes the attempt VISIBLE, which the
		// limiter never was.
		h.noteFailure(c)
		return results.SendValidationError(c, app_errors.ErrReauthInvalid.Error())
	case errors.Is(err, app_errors.ErrReauthUnavailable),
		errors.Is(err, app_errors.ErrTwoFactorNotEnrolled):
		return results.SendValidationError(c, "that confirmation method is not set up on this account")
	case errors.Is(err, app_errors.ErrReauthTooManyAttempts),
		errors.Is(err, app_errors.ErrTwoFactorTooManyAttempts):
		return results.SendError(c, http.StatusTooManyRequests,
			app_errors.ErrReauthTooManyAttempts.Error(), nil)
	default:
		return results.SendInternalServerError(c, "failed to confirm your identity", err)
	}
}
