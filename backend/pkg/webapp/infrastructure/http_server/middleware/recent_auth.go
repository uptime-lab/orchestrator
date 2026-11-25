package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// ReauthTicketHeader carries the proof. A header rather than a body field
// because it must ride along with requests whose body is not ours to touch —
// the WebAuthn ceremony payloads are verbatim protocol JSON.
const ReauthTicketHeader = "X-Reauth-Ticket"

// ReauthCodeRequired is the machine-readable marker the SPA keys the re-auth
// prompt off. It must be distinguishable from an ordinary refusal: a plain 403
// would look like "you may not do this", when the truth is "you may, once you
// confirm".
const ReauthCodeRequired = "reauth_required"

// RecentAuthVerifier is the guard the reauth service implements. The session
// proof rides along with the ticket: a confirmation is only honoured for the
// LIVE session it was issued to, so a token that outlived a sign-out or a
// password reset cannot spend one.
type RecentAuthVerifier interface {
	EnsureRecentAuth(ctx context.Context, userID uuid.UUID, ticket string, session services.SessionProof) error
}

// RequireRecentAuth demands a fresh proof of identity in front of the routes
// that change how an account is signed into.
//
// Deliberately NOT 401: the session is valid and must survive. A 401 would send
// the SPA's interceptor into a sign-out, so the user would be ejected for
// trying to secure their account.
func RequireRecentAuth(verifier RecentAuthVerifier, logger *slog.Logger) echo.MiddlewareFunc {
	log := logger.With("middleware", "recent_auth")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userID, err := GetUserIDFromContext(c)
			if err != nil {
				return results.SendUnauthorizedError(c, "unauthorized")
			}
			ticket := c.Request().Header.Get(ReauthTicketHeader)
			switch err := verifier.EnsureRecentAuth(c.Request().Context(), userID, ticket, GetSessionProofFromContext(c)); {
			case err == nil:
				return next(c)
			case errors.Is(err, app_errors.ErrReauthRequired):
				return results.SendErrorWithCode(c, http.StatusForbidden,
					app_errors.ErrReauthRequired.Error(), ReauthCodeRequired)
			case errors.Is(err, app_errors.ErrTokenInvalid), errors.Is(err, app_errors.ErrUserNotFound):
				// The SESSION is dead — signed out, revoked everywhere, or
				// minted before a password change — even though the access
				// token still verifies. This is the one case that IS a 401: a
				// re-auth prompt would be answered by a confirmation the same
				// dead session cannot obtain, so the SPA must sign out instead.
				return results.SendUnauthorizedError(c, "your session is no longer valid; sign in again")
			default:
				// Fail CLOSED. This gate stands in front of credential changes,
				// so an unresolvable user is a reason to refuse, not to wave
				// through — the opposite of the two-factor enrollment gate,
				// which fails open because it only routes an existing session.
				log.WarnContext(c.Request().Context(), "failed to verify recent authentication",
					"user_id", userID, "error", err)
				return results.SendErrorWithCode(c, http.StatusForbidden,
					app_errors.ErrReauthRequired.Error(), ReauthCodeRequired)
			}
		}
	}
}
