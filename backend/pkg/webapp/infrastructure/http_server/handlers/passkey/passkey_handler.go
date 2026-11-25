package passkey

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	passkey_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/passkey"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// LoginCompleter turns a verified identity into a session. Implemented by the
// auth service, which owns account selection and token issuance.
type LoginCompleter interface {
	CompletePasskeyLogin(userID uuid.UUID) (*services.LoginResult, error)
}

// LoginResponder renders a login result. Shared with the auth handler so a
// passkey login answers byte-for-byte like a password login — including the
// account-selection challenge for multi-account users.
type LoginResponder interface {
	SendLoginResult(c echo.Context, result *services.LoginResult) error
}

// FailedAuthRecorder is the one thing a credential-verifying handler needs from
// the auth service: somewhere to report a rejection, so the burst detector sees
// this door as well as the password form. Narrow on purpose — a one-method port
// is what makes "every credential path reports" cheap enough to actually hold.
type FailedAuthRecorder interface {
	NoteFailedLogin(ctx context.Context, sourceIP string)
}

// Handler serves the WebAuthn ceremonies.
type Handler struct {
	service   *passkey_service.Service
	completer LoginCompleter
	responder LoginResponder
	failures  FailedAuthRecorder
	logger    *slog.Logger
}

// noteFailure reports one rejected assertion. Nil-tolerant so a caller that has
// not wired the recorder loses the signal rather than the login path.
func (h *Handler) noteFailure(c echo.Context) {
	if h.failures == nil {
		return
	}
	h.failures.NoteFailedLogin(c.Request().Context(), c.RealIP())
}

func NewHandler(
	service *passkey_service.Service,
	completer LoginCompleter,
	responder LoginResponder,
	failures FailedAuthRecorder,
	logger *slog.Logger,
) *Handler {
	return &Handler{service: service, completer: completer, responder: responder, failures: failures, logger: logger.With("handler", "passkey")}
}

// RegisterRoutes wires management (authenticated) and login (public).
//
// recentAuth guards REGISTRATION only. Adding a credential is what a hijacked
// session would do to make itself permanent, and a later password reset does
// not remove enrolled authenticators — so the addition, not the listing, is
// what must be proved. Deletion is left ungated deliberately: it only ever
// reduces access, the service already refuses to remove a user's last way in,
// and demanding a proof to REMOVE a key an attacker planted would protect the
// attacker.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc) {
	g := e.Group("/api/v1/users/me/passkeys")
	g.Use(authMiddleware)
	g.GET("", h.List)
	g.POST("/register/begin", h.BeginRegistration, recentAuth)
	g.POST("/register/finish", h.FinishRegistration, recentAuth)
	g.DELETE("/:id", h.Delete)

	// Public login ceremony. Rate-limited like the other unauthenticated
	// endpoints: the cryptography is not brute-forcible, the free work is.
	limiter := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	})
	e.POST("/auth/passkey/begin", h.BeginLogin, limiter)
	e.POST("/auth/passkey/finish", h.FinishLogin, limiter)
}

// List returns the user's registered passkeys.
// @Summary List passkeys
// @Description Returns the signed-in user's registered passkeys. Public data only — a credential is a public key and a handle.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.PasskeyResult} "Registered passkeys"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /api/v1/users/me/passkeys [get]
func (h *Handler) List(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	items, err := h.service.List(c.Request().Context(), userID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list passkeys", err)
	}
	return results.SendOKResponse(c, results.NewPasskeyResults(items), "passkeys retrieved")
}

// BeginRegistration starts adding a passkey.
// @Summary Begin passkey registration
// @Description Returns the WebAuthn creation options to pass to navigator.credentials.create(), plus a single-use ticket to hand back on finish. The challenge itself is held server-side.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Creation options and ticket"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Router /api/v1/users/me/passkeys/register/begin [post]
func (h *Handler) BeginRegistration(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	creation, ticket, err := h.service.BeginRegistration(c.Request().Context(), c.Request(), userID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to start passkey registration", err)
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"ticket":  ticket,
		"options": creation.Response,
	}, "passkey registration started")
}

type finishRegistrationRequest struct {
	Ticket string `json:"ticket"`
	Name   string `json:"name"`
}

// FinishRegistration verifies the attestation and stores the credential.
// @Summary Finish passkey registration
// @Description Verifies the authenticator's attestation and stores the credential. The raw WebAuthn response is read from the request body alongside the ticket.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Success 201 {object} results.APIResponse{data=results.PasskeyResult} "Passkey registered"
// @Failure 400 {object} results.APIResponse "Registration failed"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Failure 410 {object} results.APIResponse "The request expired"
// @Router /api/v1/users/me/passkeys/register/finish [post]
func (h *Handler) FinishRegistration(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	// The ticket and label travel as query parameters so the BODY stays exactly
	// the WebAuthn credential JSON the library parses — wrapping it would mean
	// re-serializing attestation data, which is signed and must not be touched.
	ticket := c.QueryParam("ticket")
	if ticket == "" {
		return results.SendValidationError(c, "ticket is required")
	}
	// The session proof travels to persistence: the credential is stored
	// only while THIS session is still live, re-checked on the user's row
	// lock, the way the OIDC link callback does.
	record, err := h.service.FinishRegistration(c.Request().Context(), c.Request(), userID, ticket, c.QueryParam("name"),
		middleware.GetSessionProofFromContext(c))
	switch {
	case err == nil:
		return results.SendCreatedResponse(c, results.NewPasskeyResult(record), "passkey registered")
	case errors.Is(err, app_errors.ErrTokenInvalid):
		// The session ended between the step-up guard and the insert; the
		// SPA signs out on a 401, as it does at the guard itself.
		return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
	case errors.Is(err, app_errors.ErrPasskeyCeremonyExpired):
		return results.SendError(c, http.StatusGone, "this request expired; start again", nil)
	case errors.Is(err, app_errors.ErrPasskeyRegistrationFailed):
		return results.SendValidationError(c, "the authenticator's response could not be verified")
	default:
		return results.SendInternalServerError(c, "failed to register the passkey", err)
	}
}

// Delete removes a passkey.
// @Summary Delete a passkey
// @Description Removes one registered passkey. Refuses to remove the last one when the account has no password, which would leave no way to sign in.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Passkey ID"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 409 {object} results.APIResponse "This is the only way to sign in"
// @Router /api/v1/users/me/passkeys/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	passkeyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid passkey ID")
	}
	err = h.service.Delete(c.Request().Context(), userID, passkeyID)
	switch {
	case err == nil:
		return c.NoContent(http.StatusNoContent)
	case errors.Is(err, app_errors.ErrPasskeyLastCredential):
		return results.SendError(c, http.StatusConflict,
			"this is the only way you can sign in; add a password or another passkey first", nil)
	case errors.Is(err, app_errors.ErrPasskeyNotFound):
		return results.SendNotFoundError(c, "passkey not found")
	default:
		return results.SendInternalServerError(c, "failed to remove the passkey", err)
	}
}

// BeginLogin starts a passwordless sign-in.
// @Summary Begin passkey login
// @Description Returns WebAuthn request options for navigator.credentials.get(), plus a single-use ticket. No user is named: the authenticator resolves which account to offer, which is what makes this a full sign-in rather than a second factor.
// @Tags auth
// @Produce json
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Request options and ticket"
// @Failure 429 {object} results.APIResponse "Too many requests"
// @Router /auth/passkey/begin [post]
func (h *Handler) BeginLogin(c echo.Context) error {
	assertion, ticket, err := h.service.BeginLogin(c.Request())
	if err != nil {
		return results.SendInternalServerError(c, "failed to start passkey login", err)
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"ticket":  ticket,
		"options": assertion.Response,
	}, "passkey login started")
}

// FinishLogin verifies the assertion and issues a session.
// @Summary Finish passkey login
// @Description Verifies the authenticator's assertion and answers exactly like /auth/login: 200 with tokens, or 202 with the account-selection challenge for a multi-account user.
// @Tags auth
// @Accept json
// @Produce json
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Login successful"
// @Success 202 {object} results.APIResponse{data=results.LoginChallengeResult} "Account selection required"
// @Failure 401 {object} results.APIResponse "Verification failed"
// @Failure 410 {object} results.APIResponse "The request expired"
// @Router /auth/passkey/finish [post]
func (h *Handler) FinishLogin(c echo.Context) error {
	ticket := c.QueryParam("ticket")
	if ticket == "" {
		return results.SendValidationError(c, "ticket is required")
	}
	usr, err := h.service.FinishLogin(c.Request().Context(), c.Request(), ticket)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrPasskeyCeremonyExpired):
			return results.SendError(c, http.StatusGone, "this sign-in attempt expired; start again", nil)
		case errors.Is(err, app_errors.ErrPasskeyCloneWarning):
			// Worth naming explicitly: the user needs to know their
			// authenticator may be compromised, not just that login failed.
			// Counted too — a clone warning is the strongest single signal in
			// this handler that something is wrong with a credential.
			h.noteFailure(c)
			return results.SendUnauthorizedError(c,
				"this passkey looks like it has been copied; remove it and register a new one")
		default:
			// This is a full public login path that answers 401 without ever
			// touching the auth handler's funnel, so it was invisible to the
			// burst detector until now.
			h.noteFailure(c)
			return results.SendUnauthorizedError(c, "that passkey could not be verified")
		}
	}

	result, err := h.completer.CompletePasskeyLogin(usr.ID)
	if err != nil {
		return results.SendUnauthorizedError(c, "no active membership")
	}
	return h.responder.SendLoginResult(c, result)
}
