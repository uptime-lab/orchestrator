package user

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_login"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_login_refresh"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// AuthHandler handles authentication-related HTTP requests
type AuthHandler struct {
	logger      *slog.Logger
	authService services.AuthService
	cfg         *config.Config
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler(logger *slog.Logger, authService services.AuthService, cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		logger:      logger.With("handler", "auth"),
		authService: authService,
		cfg:         cfg,
	}
}

// Register registers auth routes. select-account is public (it authenticates
// with the pre-auth token in the body); switch-account and the accounts list
// require an access token.
func (h *AuthHandler) Register(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	// Per-IP rate limit on the internet-facing auth endpoints: throttles
	// credential stuffing, invite-code brute force and bcrypt CPU exhaustion.
	// ~30 req/min sustained with a burst of 10 — well above legit interactive
	// use, far below a useful brute-force rate. RealIP honours Traefik's
	// X-Forwarded-For.
	authRateLimit := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
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
	auth := e.Group("/auth", authRateLimit)
	auth.POST("/signup", h.Signup)
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.RefreshToken)
	auth.POST("/logout", h.Logout)
	auth.POST("/2fa", h.CompleteTwoFactor)
	auth.POST("/select-account", h.SelectAccount)
	auth.POST("/switch-account", h.SwitchAccount, authMiddleware)
	auth.GET("/accounts", h.ListAccounts, authMiddleware)
	// Session inventory: scoped to the caller, so it needs the access token.
	auth.GET("/sessions", h.ListSessions, authMiddleware)
	auth.DELETE("/sessions/:id", h.RevokeSession, authMiddleware)
	auth.POST("/sessions/revoke-others", h.RevokeOtherSessions, authMiddleware)
}

// Signup handles user registration
// @Summary Register a new user
// @Description Creates a new user account with email and password
// @Tags auth
// @Accept json
// @Produce json
// @Param user body user_register.RegisterCommand true "User registration data"
// @Success 201 {object} results.APIResponse{data=results.AuthResult} "User registered successfully"
// @Success 202 {object} results.APIResponse{data=SignupPendingResult} "Open registration: confirm the address from the mail, then sign in. No session; the same answer whether or not the address already had an account (nothing is mailed then)"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Registration is closed (code registration_closed): the instance has accounts and neither open registration nor an invite code is configured, its account ceiling is reached, or the count could not be read; the first owner of an empty instance needs no code, and neither does any sign-up while open registration is on. Decided before the address is looked up, so it is the answer whether or not the address is registered (as is 400 for a wrong invite code)"
// @Failure 409 {object} results.APIResponse "User already exists (only to a caller admitted to register on the first-owner or invite-code path; never on the open path)"
// @Failure 429 {object} results.APIResponse "Open registration: too many sign-ups are waiting for confirmation (code registration_busy), or the per-IP limit; try again later"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/signup [post]
func (h *AuthHandler) Signup(c echo.Context) error {
	var cmd user_register.RegisterCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind signup request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Validate command
	if err := cmd.Validate(); err != nil {
		h.logger.Error("signup validation failed", "error", err)
		return results.SendValidationError(c, err.Error())
	}

	// Register user
	registered, err := h.authService.Register(cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "signup failed", err,
			"address_ref", services.AddressLogRef(cmd.Email))
		switch {
		case errors.Is(err, app_errors.ErrUserAlreadyExists):
			return results.SendError(c, http.StatusConflict, "user already exists", err)
		case errors.Is(err, app_errors.ErrInvalidInviteCode):
			return results.SendValidationError(c, "invalid invite code")
		default:
			// A closed registration (RegistrationClosedError) is answered
			// here as itself — 403 with its own code and message — because
			// it implements OperatorFacingError; no arm, so a second
			// handler returning it later cannot flatten it into a 500.
			return results.SendInternalServerError(c, "registration failed", err)
		}
	}

	// An open registration answers the same way for a fresh address and for
	// one that already had an account, and carries no session: the account
	// is usable only after its address is confirmed.
	if registered.VerificationPending {
		return results.SendSuccess(c, http.StatusAccepted, SignupPendingResult{VerificationPending: true},
			"check your email to confirm the address and finish signing up")
	}

	h.authService.RecordSessionDevice(c.Request().Context(), registered.Tokens.RefreshToken, deviceOf(c))
	authResult := results.NewAuthResult(registered.Tokens, registered.User)
	return results.SendCreatedResponse(c, authResult, "user registered successfully")
}

// SignupPendingResult is the answer to an open registration: no session, and
// the same answer whether or not the address already had an account.
type SignupPendingResult struct {
	VerificationPending bool `json:"verification_pending"`
}

// Login handles user login with email and password
// @Summary Login with email and password
// @Description Authenticates a user. Single-account users receive tokens immediately; multi-account users receive a pre-auth token plus the account list and must call /auth/select-account.
// @Tags auth
// @Accept json
// @Produce json
// @Param credentials body user_login.UserLoginCommand true "Login credentials"
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Login successful (single account)"
// @Success 202 {object} results.APIResponse{data=results.LoginChallengeResult} "Account selection required (data=results.LoginChallengeResult); with a second factor owed the 202 carries results.TwoFactorChallengeResult and message two_factor_required instead — its two_factor_methods lists totp (finish via /auth/2fa) and/or passkey (finish via the ordinary passkey login). A registered passkey counts as an enrolled factor for members of an account that requires two factors."
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid credentials"
// @Failure 429 {object} results.APIResponse "Too many failed attempts for this address (code login_throttled, Retry-After header); answered before the password is checked"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/login [post]
func (h *AuthHandler) Login(c echo.Context) error {
	var cmd user_login.UserLoginCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind login request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Validate command
	if err := cmd.Validate(); err != nil {
		h.logger.Error("login validation failed", "error", err)
		return results.SendValidationError(c, err.Error())
	}

	// Login user
	result, err := h.authService.Login(cmd.Email, cmd.Password, c.RealIP())
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "login failed", err,
			"address_ref", services.AddressLogRef(cmd.Email))
		return h.sendLoginError(c, err)
	}

	return h.sendLoginResult(c, result)
}

// sendLoginResult renders the discriminated login result: 200 with tokens or
// 202 with the account-selection challenge.
// SendLoginResult is the exported form used by the passkey handler, so a
// passkey login answers byte-for-byte like a password login.
func (h *AuthHandler) SendLoginResult(c echo.Context, result *services.LoginResult) error {
	return h.sendLoginResult(c, result)
}

func (h *AuthHandler) sendLoginResult(c echo.Context, result *services.LoginResult) error {
	// The second-factor challenge is checked FIRST: when it is set, neither
	// tokens nor an account list exist yet.
	if result.TwoFactorToken != "" {
		return results.SendSuccess(c, http.StatusAccepted,
			results.NewTwoFactorChallengeResult(result), "two_factor_required")
	}
	if result.Tokens != nil {
		// Label the session with the device that started it, so its owner can
		// recognise it later in the session list. Best-effort by design.
		h.authService.RecordSessionDevice(c.Request().Context(), result.Tokens.RefreshToken, deviceOf(c))
		return results.SendOKResponse(c, results.NewAuthResult(result.Tokens, result.User), "login successful")
	}
	return results.SendSuccess(c, http.StatusAccepted, results.NewLoginChallengeResult(result), "account selection required")
}

// sendLoginError maps login errors to responses. Membership problems are 401
// with a message explaining the problem (never 403 — that is reserved for
// per-action authorization denials).
func (h *AuthHandler) sendLoginError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, app_errors.ErrInvalidCredentials):
		// Counted here rather than in the service: the source address is known
		// only to the HTTP layer, the same reason session device labels are
		// attached here. This is the funnel every rejected credential passes
		// through, so a new login path cannot forget to be watched.
		h.authService.NoteFailedLogin(c.Request().Context(), c.RealIP())
		return results.SendUnauthorizedError(c, "invalid credentials")
	case errors.Is(err, app_errors.ErrMembershipDisabled):
		return results.SendUnauthorizedError(c, "Your membership in this account is disabled. Ask an account administrator to re-enable it.")
	case errors.Is(err, app_errors.ErrNotAMember):
		return results.SendUnauthorizedError(c, "You do not have an active account membership. Ask an account administrator for an invitation.")
	case errors.Is(err, app_errors.ErrTokenInvalid):
		// The credential was verified, and then replaced — or the session
		// ended — before the session could be recorded (Issue refused under
		// the row lock). Nothing was stored; the caller signs
		// in with the credential that is current now.
		return results.SendUnauthorizedError(c, "Sign in again. Your credentials changed while you were signing in.")
	case errors.As(err, new(*app_errors.LoginThrottledError)):
		return sendLoginThrottled(c, err)
	case errors.Is(err, app_errors.ErrTwoFactorTooManyAttempts):
		// Temporary, and not a server fault: the user waits out the window.
		// Not counted by the burst detector — the wrong codes that led here
		// already were.
		return results.SendErrorWithCode(c, http.StatusTooManyRequests,
			app_errors.ErrTwoFactorTooManyAttempts.Error(), app_errors.TwoFactorLockedErrorCode)
	default:
		return results.SendInternalServerError(c, "login failed", err)
	}
}

// SendLoginThrottled answers a per-address password brake: 429 with code
// login_throttled and a Retry-After header.
func sendLoginThrottled(c echo.Context, err error) error {
	var throttled *app_errors.LoginThrottledError
	if !errors.As(err, &throttled) {
		return results.SendInternalServerError(c, "login failed", err)
	}
	c.Response().Header().Set("Retry-After", strconv.Itoa(throttled.RetryAfterSeconds()))
	return results.SendErrorWithCode(c, http.StatusTooManyRequests, throttled.Error(), app_errors.LoginThrottledErrorCode)
}

// selectAccountRequest carries the pre-auth token and the chosen account.
type selectAccountRequest struct {
	PreToken  string    `json:"pre_token"`
	AccountID uuid.UUID `json:"account_id"`
}

// SelectAccount exchanges a pre-auth token for an account-pinned token pair
// @Summary Select an account after login
// @Description Exchanges the pre-auth token returned by /auth/login for a token pair pinned to the chosen account. The token is single-use, expires five minutes after the login, and is refused after a password change made since it was issued — a fresh login is then required.
// @Tags auth
// @Accept json
// @Produce json
// @Param selection body selectAccountRequest true "Pre-auth token and account ID"
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Account selected"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid pre-auth token or membership"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/select-account [post]
func (h *AuthHandler) SelectAccount(c echo.Context) error {
	var req selectAccountRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if req.PreToken == "" || req.AccountID == uuid.Nil {
		return results.SendValidationError(c, "pre_token and account_id are required")
	}

	tokens, usr, err := h.authService.SelectAccount(c.Request().Context(), req.PreToken, req.AccountID)
	if err != nil {
		h.logger.Error("account selection failed", "error", err)
		switch {
		case errors.Is(err, app_errors.ErrTokenInvalid), errors.Is(err, app_errors.ErrTokenExpired),
			errors.Is(err, app_errors.ErrUserNotFound):
			return results.SendUnauthorizedError(c, "invalid or expired pre-auth token")
		case errors.Is(err, app_errors.ErrMembershipDisabled):
			return results.SendUnauthorizedError(c, "Your membership in this account is disabled. Ask an account administrator to re-enable it.")
		case errors.Is(err, app_errors.ErrNotAMember):
			return results.SendUnauthorizedError(c, "You do not have an active account membership. Ask an account administrator for an invitation.")
		default:
			return results.SendInternalServerError(c, "account selection failed", err)
		}
	}

	h.authService.RecordSessionDevice(c.Request().Context(), tokens.RefreshToken, deviceOf(c))
	return results.SendOKResponse(c, results.NewAuthResult(tokens, usr), "account selected")
}

// switchAccountRequest carries the target account of an org switch.
type switchAccountRequest struct {
	AccountID uuid.UUID `json:"account_id"`
}

// SwitchAccount mints a token pair for another account of the current user
// @Summary Switch to another account
// @Description Mints a token pair pinned to another account the current user is an active member of
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param target body switchAccountRequest true "Target account ID"
// @Success 200 {object} results.APIResponse{data=results.TokenRefreshResult} "Account switched"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized, session no longer active, or not a member"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/switch-account [post]
func (h *AuthHandler) SwitchAccount(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var req switchAccountRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if req.AccountID == uuid.Nil {
		return results.SendValidationError(c, "account_id is required")
	}

	tokens, err := h.authService.SwitchAccount(c.Request().Context(), userID, req.AccountID,
		middleware.GetSessionProofFromContext(c))
	if err != nil {
		h.logger.Error("account switch failed", "error", err, "user_id", userID)
		switch {
		case errors.Is(err, app_errors.ErrTokenInvalid):
			// The access token still verifies, but the session it names was
			// signed out or predates a password change. No new credential.
			return results.SendUnauthorizedError(c, "Sign in again. This session is no longer active.")
		case errors.Is(err, app_errors.ErrMembershipDisabled):
			return results.SendUnauthorizedError(c, "Your membership in this account is disabled. Ask an account administrator to re-enable it.")
		case errors.Is(err, app_errors.ErrNotAMember), errors.Is(err, app_errors.ErrUserNotFound):
			return results.SendUnauthorizedError(c, "You do not have an active account membership. Ask an account administrator for an invitation.")
		default:
			return results.SendInternalServerError(c, "account switch failed", err)
		}
	}

	return results.SendOKResponse(c, results.NewTokenRefreshResult(tokens), "account switched")
}

// ListAccounts returns the accounts the current user can switch into
// @Summary List the current user's accounts
// @Description Returns the accounts the current user holds an active membership in (feeds the org switcher)
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.AccountOptionResult} "Accounts"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/accounts [get]
func (h *AuthHandler) ListAccounts(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	options, err := h.authService.ListAccounts(c.Request().Context(), userID)
	if err != nil {
		h.logger.Error("failed to list accounts", "error", err, "user_id", userID)
		return results.SendInternalServerError(c, "failed to list accounts", err)
	}

	return results.SendOKResponse(c, results.NewAccountOptionResults(options), "accounts retrieved successfully")
}

// RefreshToken handles token refresh
// @Summary Refresh access token
// @Description Refreshes the access token using a valid refresh token. The account stays pinned; a revoked or disabled membership is rejected with a machine-readable 401.
// @Tags auth
// @Accept json
// @Produce json
// @Param refresh body user_login_refresh.UserLoginRefreshCommand true "Token refresh data"
// @Success 200 {object} results.APIResponse{data=results.TokenRefreshResult} "Tokens refreshed successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid or expired refresh token, or membership revoked/disabled"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/refresh [post]
func (h *AuthHandler) RefreshToken(c echo.Context) error {
	var cmd user_login_refresh.UserLoginRefreshCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind refresh request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Validate command
	if err := cmd.Validate(); err != nil {
		h.logger.Error("refresh validation failed", "error", err)
		return results.SendValidationError(c, err.Error())
	}

	// Refresh tokens
	tokens, err := h.authService.RefreshTokenFromDevice(cmd.RefreshToken, deviceOf(c))
	if err != nil {
		// A refused refresh token (expired, revoked, signed out) is the API
		// working: WARN, like a wrong password. Only a failure of the server
		// itself is ERROR.
		results.LogFailure(c.Request().Context(), h.logger, "token refresh failed", err)
		switch {
		case errors.Is(err, app_errors.ErrTokenInvalid), errors.Is(err, app_errors.ErrTokenExpired),
			errors.Is(err, app_errors.ErrUserNotFound):
			return results.SendUnauthorizedError(c, "invalid or expired refresh token")
		case errors.Is(err, app_errors.ErrMembershipDisabled):
			return results.SendUnauthorizedError(c, "Your membership in this account is disabled. Ask an account administrator to re-enable it.")
		case errors.Is(err, app_errors.ErrNotAMember):
			return results.SendUnauthorizedError(c, "You do not have an active account membership. Ask an account administrator for an invitation.")
		default:
			return results.SendInternalServerError(c, "token refresh failed", err)
		}
	}

	refreshResult := results.NewTokenRefreshResult(tokens)
	return results.SendOKResponse(c, refreshResult, "tokens refreshed successfully")
}

type twoFactorRequest struct {
	TwoFactorToken string `json:"two_factor_token"`
	Code           string `json:"code"`
}

// CompleteTwoFactor finishes a login that required a second factor.
// @Summary Complete a two-factor login
// @Description Exchanges the ticket from a `two_factor_required` login response, plus a TOTP or single-use recovery code, for a session. Only for challenges whose two_factor_methods include totp; a passkey-only challenge is completed through the passkey login endpoints and this route refuses it. Answers exactly like /auth/login does: 200 with tokens, or 202 with an account-selection challenge for a multi-account user.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body twoFactorRequest true "Ticket and code"
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Login successful"
// @Success 202 {object} results.APIResponse{data=results.LoginChallengeResult} "Account selection required"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid ticket or code"
// @Failure 429 {object} results.APIResponse "Too many wrong codes recently (code two_factor_locked); wait and sign in again"
// @Router /auth/2fa [post]
func (h *AuthHandler) CompleteTwoFactor(c echo.Context) error {
	var req twoFactorRequest
	if err := c.Bind(&req); err != nil || req.TwoFactorToken == "" || req.Code == "" {
		return results.SendValidationError(c, "two_factor_token and code are required")
	}
	result, err := h.authService.CompleteTwoFactor(c.Request().Context(), req.TwoFactorToken, req.Code)
	if err != nil {
		// A wrong code and an expired ticket answer identically: this endpoint
		// is public, and distinguishing them would tell an attacker which half
		// of the credential they already hold.
		if errors.Is(err, app_errors.ErrTwoFactorInvalidCode) ||
			// A replayed code answers exactly like a wrong one HERE, unlike the
			// authenticated paths: this endpoint is public, and saying "that
			// code was already used" confirms to an attacker that the six
			// digits they captured were the right ones.
			errors.Is(err, app_errors.ErrTwoFactorCodeAlreadyUsed) ||
			errors.Is(err, app_errors.ErrTokenInvalid) ||
			errors.Is(err, app_errors.ErrTokenExpired) ||
			errors.Is(err, app_errors.ErrTwoFactorNotEnrolled) {
			// Counted, because this path returns its own 401 without going
			// through sendLoginError. Until this line the burst detector watched
			// the password form only — so an attacker holding a password and
			// grinding six digits was the one case it could not see, which is
			// exactly the case where the second factor is all that is left.
			h.authService.NoteFailedLogin(c.Request().Context(), c.RealIP())
			return results.SendUnauthorizedError(c, "that code is not valid, or the sign-in attempt expired — start again")
		}
		return h.sendLoginError(c, err)
	}
	return h.sendLoginResult(c, result)
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout revokes the caller's session server-side.
// @Summary Log out
// @Description Revokes the supplied refresh token and every successor rotated out of it, so the session cannot be resumed. Always answers 200: a token that is already invalid is, from the caller's point of view, already logged out. Discarding the access token is still the client's job — it stays valid until it expires.
// @Tags auth
// @Accept json
// @Produce json
// @Param body body logoutRequest true "Refresh token to revoke"
// @Success 200 {object} results.APIResponse{data=map[string]bool} "Session revoked"
// @Router /auth/logout [post]
func (h *AuthHandler) Logout(c echo.Context) error {
	var req logoutRequest
	if err := c.Bind(&req); err != nil || req.RefreshToken == "" {
		// Nothing to revoke is not a failure worth reporting to a client that
		// is on its way out anyway.
		return results.SendOKResponse(c, map[string]bool{"revoked": false}, "logged out")
	}
	if err := h.authService.RevokeSession(c.Request().Context(), req.RefreshToken); err != nil {
		h.logger.Error("failed to revoke session", "error", err)
		return results.SendInternalServerError(c, "failed to log out", err)
	}
	return results.SendOKResponse(c, map[string]bool{"revoked": true}, "logged out")
}
