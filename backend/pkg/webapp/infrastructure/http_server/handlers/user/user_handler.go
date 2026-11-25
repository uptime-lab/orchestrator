package user

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_password_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/user_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// UserHandler handles user-related HTTP requests
type UserHandler struct {
	userService services.UserService
	logger      *slog.Logger
}

// NewUserHandler creates a new user handler
func NewUserHandler(userService services.UserService, logger *slog.Logger) *UserHandler {
	return &UserHandler{
		userService: userService,
		logger:      logger.With("handler", "user"),
	}
}

// RegisterRoutes registers user routes.
//
// recentAuth guards the password change. It previously took only a session and
// a new password — no current password at all — so anyone with a live session
// could rewrite the credential and, since a change invalidates older refresh
// tokens, push the real owner out with it.
//
// It guards the PROFILE update for the same reason, one line above, which was
// missed the first time because "edit your name" does not read as a credential
// change. It is one: the same payload carries the email, the email IS the
// sign-in identifier, and password reset resolves by the stored address and
// revokes every session when it completes. So a hijacked session that repoints
// the address takes the account and evicts its owner using the recovery flow
// itself.
func (h *UserHandler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	users := e.Group("/api/v1/users")
	users.Use(authMiddleware)

	// The roster is tenant data and has no natural action — the sibling
	// GET /api/v1/members answers "any ACTIVE member may read it", and this
	// route must answer the same. Without the check the JWT middleware is the
	// only gate and it never resolves membership, so a removed member kept
	// reading the roster until their access token expired, and a
	// member holding no binding read it permanently.
	users.GET("", h.ListUsers, middleware.RequireActiveMember(enforcer))
	users.GET("/me", h.GetMe)
	users.PUT("/me", h.UpdateMe, recentAuth)
	users.PUT("/me/password", h.UpdatePassword, recentAuth)
	// Accepting the operator's terms is a statement about oneself, not a
	// privileged action: plain auth, no recentAuth step-up and no authz action.
	// A sign-up records the acceptance itself (terms_accepted on /auth/signup);
	// this route records it for a user who is already signed in, and is what
	// clients written before that field call.
	//
	// It is rate limited anyway because each call appends to the append-only
	// audit log, and a legitimate client sends it once — per user, not per IP,
	// since the log it can grow is the account's.
	users.POST("/me/beta-acknowledgement", h.AcknowledgeBetaNotice, betaAckRateLimit())
}

// betaAckRateLimit throttles the acknowledgement endpoint. Keyed on the user id
// (populated by the group's auth middleware, which runs first) and falling back
// to the request IP if that is somehow missing, so a failure to extract can
// never mean "unlimited".
func betaAckRateLimit() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.2),
			Burst:     5,
			ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			if userID, err := middleware.GetUserIDFromContext(c); err == nil {
				return userID.String(), nil
			}
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "too many requests, slow down", nil)
		},
	})
}

// ListUsers lists all users of the requesting account
// @Summary List account users
// @Description Retrieves all users belonging to the authenticated user's account. Any ACTIVE member may read the roster; a removed or disabled one may not.
// @Tags users
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.UserListResult} "Users retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/users [get]
func (h *UserHandler) ListUsers(c echo.Context) error {
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

	query := user_list.UserListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	users, err := h.userService.ListByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list users", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list users", err)
	}

	return results.SendOKResponse(c, results.NewUserListResult(users), "users retrieved successfully")
}

// GetMe retrieves the current user
// @Summary Get current user
// @Description Retrieves the authenticated user's profile information
// @Tags users
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.UserResult} "User retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "User not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/users/me [get]
func (h *UserHandler) GetMe(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	user, err := h.userService.GetByID(c.Request().Context(), userID)
	if err != nil {
		h.logger.Error("failed to get user", "error", err, "user_id", userID)
		if errors.Is(err, app_errors.ErrUserNotFound) {
			return results.SendNotFoundError(c, "user not found")
		}
		return results.SendInternalServerError(c, "failed to get user", err)
	}

	userResult := results.NewUserResult(user)
	return results.SendOKResponse(c, userResult, "user retrieved successfully")
}

// UpdateMe updates the current user's profile
// @Summary Update current user
// @Description Updates the authenticated user's profile. This is a PARTIAL update: an omitted field is left unchanged, so sending only "name" does not touch the email. An explicitly empty or unparseable email is refused with 400 rather than stored — the email is the sign-in and password-reset identifier, and blanking it would lock the account out with no self-service way back. Changing the email to a different address clears its verified state and sends a fresh verification mail. Because that address is the sign-in identifier, this route requires a step-up confirmation: send the ticket in X-Reauth-Ticket, or expect 403 with code reauth_required.
// @Tags users
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket from /api/v1/users/me/reauth"
// @Param user body user_update.UserUpdateCommand true "Profile fields to change; omit a field to leave it as it is"
// @Success 200 {object} results.APIResponse{data=results.UserResult} "User updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "User not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/users/me [put]
func (h *UserHandler) UpdateMe(c echo.Context) error {
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

	var cmd user_update.UserUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context in command
	cmd.ID = userID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// The session proof travels to persistence: the email is the sign-in
	// identifier, so it is written only while THIS session is still live,
	// re-checked on the user's row lock.
	user, err := h.userService.UpdateMe(c.Request().Context(), cmd, middleware.GetSessionProofFromContext(c))
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update user", err, "user_id", userID)
		if errors.Is(err, app_errors.ErrUserNotFound) {
			return results.SendNotFoundError(c, "user not found")
		}
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			// The session ended between the step-up guard and the write; the
			// SPA signs out on a 401, as it does at the guard itself.
			return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
		}
		// The command's own validation. Without these arms a rejected profile
		// update reports 500, which reads as "the server is broken" for what is
		// entirely the caller's input.
		if errors.Is(err, app_errors.ErrUserEmailRequired) ||
			errors.Is(err, app_errors.ErrUserEmailInvalid) ||
			errors.Is(err, app_errors.ErrUserNameTooLong) ||
			errors.Is(err, app_errors.ErrUserNameRequired) {
			return results.SendValidationError(c, err.Error())
		}
		if errors.Is(err, app_errors.ErrUnauthorized) {
			return results.SendUnauthorizedError(c, "unauthorized")
		}
		return results.SendInternalServerError(c, "failed to update user", err)
	}

	userResult := results.NewUserResult(user)
	return results.SendOKResponse(c, userResult, "user updated successfully")
}

// UpdatePassword updates the user's password
// @Summary Update user password
// @Description Updates the authenticated user's password
// @Tags users
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param password body user_password_update.UserUpdatePasswordCommand true "Password update data"
// @Success 200 {object} results.APIResponse{data=results.UserResult} "Password updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "User not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/users/me/password [put]
func (h *UserHandler) UpdatePassword(c echo.Context) error {
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

	var cmd user_password_update.UserUpdatePasswordCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context in command
	cmd.ID = userID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	// Under the session proof, as UpdateMe: a password change from a session
	// that died between the step-up guard and the write is refused.
	updatedUser, err := h.userService.UpdatePassword(c.Request().Context(), cmd, middleware.GetSessionProofFromContext(c))
	if err != nil {
		h.logger.Error("failed to update password", "error", err, "user_id", userID)
		if errors.Is(err, app_errors.ErrUserNotFound) {
			return results.SendNotFoundError(c, "user not found")
		}
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
		}
		if errors.Is(err, app_errors.ErrUserPasswordRequired) ||
			errors.Is(err, app_errors.ErrUserPasswordTooShort) ||
			errors.Is(err, app_errors.ErrUserPasswordTooLong) {
			return results.SendValidationError(c, err.Error())
		}
		if errors.Is(err, app_errors.ErrUnauthorized) {
			return results.SendUnauthorizedError(c, "unauthorized")
		}
		return results.SendInternalServerError(c, "failed to update password", err)
	}

	userResult := results.NewUserResult(updatedUser)
	return results.SendOKResponse(c, userResult, "password updated successfully")
}

// AcknowledgeBetaNotice records acceptance of the operator's terms of use
// @Summary Accept the terms of use
// @Description Records that the authenticated user accepted the operator's notice or terms of use, with a timestamp and an audit event. Takes no body: the accepted revision is decided server-side. Idempotent. A sign-up records the same acceptance through terms_accepted on /auth/signup.
// @Tags users
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.UserResult} "Acceptance recorded"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "User not found"
// @Failure 429 {object} results.APIResponse "Too many requests"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/users/me/beta-acknowledgement [post]
func (h *UserHandler) AcknowledgeBetaNotice(c echo.Context) error {
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

	cmd := user_beta_notice_ack.UserBetaNoticeAckCommand{
		ID:          userID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	updatedUser, err := h.userService.AcknowledgeBetaNotice(c.Request().Context(), cmd)
	if err != nil {
		h.logger.Error("failed to record terms acceptance", "error", err, "user_id", userID)
		if errors.Is(err, app_errors.ErrUserNotFound) {
			return results.SendNotFoundError(c, "user not found")
		}
		return results.SendInternalServerError(c, "failed to record acceptance", err)
	}

	userResult := results.NewUserResult(updatedUser)
	return results.SendOKResponse(c, userResult, "acceptance recorded")
}
