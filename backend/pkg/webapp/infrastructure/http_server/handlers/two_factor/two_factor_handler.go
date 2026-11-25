package two_factor

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	two_factor_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/labstack/echo/v4"
)

// Handler serves second-factor enrollment and management for the signed-in
// user. The login-time verification lives on the auth handler, because it runs
// BEFORE there is a session.
type Handler struct {
	service *two_factor_service.Service
	logger  *slog.Logger
}

func NewHandler(service *two_factor_service.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger.With("handler", "two_factor")}
}

// RegisterRoutes wires enrollment and management.
//
// recentAuth guards ENROLLMENT: a hijacked session must not be able to bind its
// own authenticator to the account. Disable is not gated here because it
// already demands a current code, which is a stronger proof of possession than
// a ticket would be.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc) {
	g := e.Group("/api/v1/users/me/2fa")
	g.Use(authMiddleware)
	g.GET("", h.Status)
	g.POST("/enroll", h.Enroll, recentAuth)
	g.POST("/confirm", h.Confirm, recentAuth)
	g.DELETE("", h.Disable)
}

type codeRequest struct {
	Code string `json:"code"`
}

// Status reports whether a second factor is active.
// @Summary Two-factor status
// @Description Reports whether the signed-in user has a confirmed second factor and how many single-use recovery codes remain.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Status"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/users/me/2fa [get]
func (h *Handler) Status(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ctx := c.Request().Context()
	return results.SendOKResponse(c, map[string]interface{}{
		"enabled":                  h.service.IsEnabled(ctx, userID),
		"recovery_codes_remaining": h.service.RemainingRecoveryCodes(ctx, userID),
	}, "two-factor status retrieved")
}

// Enroll starts enrollment.
// @Summary Start two-factor enrollment
// @Description Generates a TOTP secret and returns the otpauth:// URI to render as a QR code. This does NOT enable anything — the factor stays inactive until POST /confirm proves the authenticator holds the same secret, so abandoning this flow cannot lock the user out.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Success 200 {object} results.APIResponse{data=map[string]string} "Enrollment secret and URI"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Failure 409 {object} results.APIResponse "Two-factor is already enabled"
// @Router /api/v1/users/me/2fa/enroll [post]
func (h *Handler) Enroll(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	enrollment, err := h.service.Enroll(c.Request().Context(), userID)
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]string{
			"secret":      enrollment.Secret,
			"otpauth_url": enrollment.OTPAuthURL,
			"qr_code":     enrollment.QRCodeDataURI,
		}, "scan the code with your authenticator, then confirm")
	case errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled):
		return results.SendError(c, http.StatusConflict,
			"two-factor authentication is already enabled; disable it first to enroll a new device", nil)
	default:
		return results.SendInternalServerError(c, "failed to start two-factor enrollment", err)
	}
}

// Confirm activates the pending enrollment.
// @Summary Confirm two-factor enrollment
// @Description Verifies a code from the authenticator and activates the factor. Returns the single-use recovery codes EXACTLY ONCE — they are stored only as digests and cannot be shown again.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param body body codeRequest true "Code from the authenticator"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Recovery codes (shown once)"
// @Failure 400 {object} results.APIResponse "Invalid code"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "No pending enrollment"
// @Router /api/v1/users/me/2fa/confirm [post]
func (h *Handler) Confirm(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req codeRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return results.SendValidationError(c, "code is required")
	}
	// The session proof travels to persistence: the factor is enabled only
	// while THIS session is still live, re-checked on the user's row lock,
	// the way the OIDC link callback does.
	codes, err := h.service.Confirm(c.Request().Context(), userID, req.Code, middleware.GetSessionProofFromContext(c))
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]interface{}{"recovery_codes": codes},
			"two-factor authentication is on — save these recovery codes now, they will not be shown again")
	case errors.Is(err, app_errors.ErrTokenInvalid):
		// The session ended between the step-up guard and the write; the
		// SPA signs out on a 401, as it does at the guard itself.
		return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
	case errors.Is(err, app_errors.ErrTwoFactorInvalidCode):
		return results.SendValidationError(c, "that code did not match; check your authenticator's clock and try the current code")
	case errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled):
		return results.SendError(c, http.StatusConflict, "two-factor authentication is already enabled", nil)
	case errors.Is(err, app_errors.ErrTwoFactorEnrollmentChanged):
		return results.SendError(c, http.StatusConflict, "this enrollment was restarted; scan the newest code and try again", nil)
	case errors.Is(err, app_errors.ErrTwoFactorNotEnrolled):
		return results.SendNotFoundError(c, "start enrollment first")
	default:
		return results.SendInternalServerError(c, "failed to confirm two-factor enrollment", err)
	}
}

// Disable turns the factor off.
// @Summary Disable two-factor authentication
// @Description Removes the second factor. Requires a current TOTP or recovery code, so a hijacked session cannot quietly strip the protection.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body codeRequest true "Current code"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Invalid code"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 429 {object} results.APIResponse "Too many wrong codes recently (code two_factor_locked)"
// @Router /api/v1/users/me/2fa [delete]
func (h *Handler) Disable(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req codeRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return results.SendValidationError(c, "a current code is required to disable two-factor authentication")
	}
	ctx := c.Request().Context()
	// Re-prove possession before removing the factor. Without this, a stolen
	// session — exactly what the second factor exists to survive — could turn
	// it off in one request.
	if err := h.service.Verify(ctx, userID, req.Code); err != nil {
		if errors.Is(err, app_errors.ErrTwoFactorInvalidCode) {
			return results.SendValidationError(c, "that code did not match")
		}
		// Without this arm the default below answers 500, so a double-submitted
		// form would report an internal server error for a correct code.
		if errors.Is(err, app_errors.ErrTwoFactorCodeAlreadyUsed) {
			return results.SendValidationError(c, app_errors.ErrTwoFactorCodeAlreadyUsed.Error())
		}
		if errors.Is(err, app_errors.ErrTwoFactorNotEnrolled) {
			return c.NoContent(http.StatusNoContent)
		}
		// Too many wrong codes recently: temporary, and the caller's to wait
		// out, not a server fault.
		if errors.Is(err, app_errors.ErrTwoFactorTooManyAttempts) {
			return results.SendErrorWithCode(c, http.StatusTooManyRequests,
				app_errors.ErrTwoFactorTooManyAttempts.Error(), app_errors.TwoFactorLockedErrorCode)
		}
		return results.SendInternalServerError(c, "failed to verify the code", err)
	}
	if err := h.service.Disable(ctx, userID); err != nil {
		return results.SendInternalServerError(c, "failed to disable two-factor authentication", err)
	}
	return c.NoContent(http.StatusNoContent)
}
