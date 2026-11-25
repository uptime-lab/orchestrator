package email_verification

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/email_verification"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Handler serves the email-verification endpoints: a public confirm (the link
// in the email) and an authenticated resend.
type Handler struct {
	service *email_verification.Service
	logger  *slog.Logger
}

func NewHandler(service *email_verification.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger.With("handler", "email_verification")}
}

// RegisterRoutes wires both endpoints. Confirm is public — the token in the
// body IS the credential — so it carries its own per-IP limiter; the group
// limiter on /auth is not inherited here.
//
// Resend carries one too, for the reason the invite routes do: the per-user
// throttle in the service is the real bound on how much mail one mailbox
// receives, but an authenticated caller can repoint their own address (PUT
// /users/me clears the verification stamp) and drive this at an arbitrary third
// party. The limiter bounds the rate a single source can drive regardless of
// which address it aims at.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	e.POST("/auth/verify-email", h.Confirm, publicRateLimit())
	e.POST("/api/v1/users/me/resend-verification", h.Resend, authMiddleware, resendRateLimit())
}

// resendRateLimit is deliberately looser than publicRateLimit: this is an
// authenticated action a real user does click twice after mistyping.
func resendRateLimit() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(1),
			Burst:     10,
			ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "too many requests, slow down", nil)
		},
	})
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

type confirmRequest struct {
	Token string `json:"token"`
}

// Confirm consumes a verification link.
// @Summary Confirm an email address
// @Description Consumes a one-time verification token from an email link and marks the address as verified. Idempotent for an already-verified user; the token itself is single-use.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body confirmRequest true "Verification token"
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Email verified"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid or expired token"
// @Failure 409 {object} results.APIResponse "Token was issued for a different address"
// @Router /auth/verify-email [post]
func (h *Handler) Confirm(c echo.Context) error {
	var req confirmRequest
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return results.SendValidationError(c, "token is required")
	}
	err := h.service.Confirm(c.Request().Context(), req.Token)
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]bool{"verified": true}, "email verified")
	case errors.Is(err, app_errors.ErrEmailVerificationStale):
		return results.SendError(c, http.StatusConflict, "this link was issued for a different email address; request a new one", nil)
	case errors.Is(err, app_errors.ErrEmailVerificationInvalid):
		// Unknown, expired and already-used tokens are indistinguishable here
		// on purpose: the endpoint is public.
		return results.SendUnauthorizedError(c, "this verification link is invalid or has expired")
	default:
		return results.SendInternalServerError(c, "failed to verify email", err)
	}
}

// Resend mails a fresh verification link to the current user.
// @Summary Resend the verification email
// @Description Mails a new verification link to the signed-in user's address, revoking any previous link. Throttled per user.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Verification email sent"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "Email is already verified"
// @Failure 429 {object} results.APIResponse "Requested too recently"
// @Failure 503 {object} results.APIResponse "Email delivery is not configured"
// @Router /api/v1/users/me/resend-verification [post]
func (h *Handler) Resend(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	err = h.service.SendForUserID(c.Request().Context(), userID)
	switch {
	case err == nil:
		return results.SendOKResponse(c, map[string]bool{"sent": true}, "verification email sent")
	case errors.Is(err, app_errors.ErrEmailAlreadyVerified):
		return results.SendError(c, http.StatusConflict, "this email address is already verified", nil)
	case errors.Is(err, app_errors.ErrEmailSendThrottled):
		return results.SendError(c, http.StatusTooManyRequests, "a verification email was sent recently; check your inbox before requesting another", nil)
	case errors.Is(err, app_errors.ErrEmailNotConfigured):
		return results.SendError(c, http.StatusServiceUnavailable, "email delivery is not configured on this deployment", nil)
	default:
		return results.SendInternalServerError(c, "failed to send verification email", err)
	}
}
