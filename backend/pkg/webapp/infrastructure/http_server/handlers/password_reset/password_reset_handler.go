package password_reset

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/password_reset"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Handler serves the two public password-reset endpoints: request a link, and
// consume it.
type Handler struct {
	service *password_reset.Service
	logger  *slog.Logger
}

func NewHandler(service *password_reset.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger.With("handler", "password_reset")}
}

// RegisterRoutes wires both endpoints. Both are public — the token in the body
// IS the credential — so they carry their own per-IP limiter; the group
// limiter on /auth is not inherited here.
func (h *Handler) RegisterRoutes(e *echo.Echo) {
	e.POST("/auth/forgot-password", h.Request, publicRateLimit())
	e.POST("/auth/reset-password", h.Confirm, publicRateLimit())
}

func publicRateLimit() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.5),
			Burst:     10,
			ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "too many requests, slow down", nil)
		},
	})
}

type requestBody struct {
	Email string `json:"email"`
}

type confirmBody struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// Request mails a password-reset link.
// @Summary Request a password reset link
// @Description Mails a one-time reset link to the address if it belongs to an account with a password. Always answers 200 with the same message — an unknown, throttled or passwordless (OIDC-only) address is indistinguishable from a successful send, so the endpoint cannot be used to discover registered users.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body requestBody true "Email address"
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Request accepted"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 429 {object} results.APIResponse "Too many requests"
// @Failure 503 {object} results.APIResponse "Email delivery is not configured"
// @Router /auth/forgot-password [post]
func (h *Handler) Request(c echo.Context) error {
	var req requestBody
	if err := c.Bind(&req); err != nil || req.Email == "" {
		return results.SendValidationError(c, "email is required")
	}
	err := h.service.Request(c.Request().Context(), req.Email)
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]bool{"sent": true},
			"if that address belongs to an account, a reset link is on its way")
	case errors.Is(err, app_errors.ErrEmailNotConfigured):
		// Safe to report: it describes the deployment, not the address.
		return results.SendError(c, http.StatusServiceUnavailable,
			"password reset by email is not configured on this deployment; ask an administrator to reset it for you", nil)
	default:
		return results.SendInternalServerError(c, "failed to request a password reset", err)
	}
}

// Confirm consumes a reset link and sets the new password.
// @Summary Set a new password from a reset link
// @Description Consumes a one-time reset token and installs the new password. Signs out every other session by invalidating refresh tokens issued before the reset, and marks the address verified. The token is single-use.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body confirmBody true "Reset token and new password"
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Password updated"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid or expired token"
// @Failure 409 {object} results.APIResponse "Token was issued for a different address"
// @Router /auth/reset-password [post]
func (h *Handler) Confirm(c echo.Context) error {
	var req confirmBody
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return results.SendValidationError(c, "token is required")
	}
	err := h.service.Confirm(c.Request().Context(), req.Token, req.Password)
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]bool{"updated": true},
			"password updated — sign in with your new password")
	case errors.Is(err, app_errors.ErrUserPasswordRequired),
		errors.Is(err, app_errors.ErrUserPasswordTooShort):
		return results.SendValidationError(c, "password must be at least 8 characters")
	case errors.Is(err, app_errors.ErrUserPasswordTooLong):
		return results.SendValidationError(c, err.Error())
	case errors.Is(err, app_errors.ErrPasswordResetStale):
		return results.SendError(c, http.StatusConflict,
			"this link was issued for a different email address; request a new one", nil)
	case errors.Is(err, app_errors.ErrPasswordResetInvalid):
		// Unknown, expired and already-used tokens are indistinguishable here
		// on purpose: the endpoint is public.
		return results.SendUnauthorizedError(c, "this reset link is invalid or has expired")
	default:
		return results.SendInternalServerError(c, "failed to reset the password", err)
	}
}
