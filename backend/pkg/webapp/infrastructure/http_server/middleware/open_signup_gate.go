package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// SettledAccountGate answers whether an account may be used: nil, or
// app_errors.ErrEmailVerificationRequired for an open sign-up whose owner
// has not confirmed their address, or a typed refusal of its own.
type SettledAccountGate interface {
	RequireSettledAccount(ctx context.Context, accountID, userID uuid.UUID) error
}

// openSignupExemptExact are the only requests a session on an unconfirmed
// open sign-up may make, matched EXACTLY and by method: what the
// confirm-your-address screen needs to render (the signed-in user, their
// permissions, the install's capabilities) and the resend. Nothing here
// writes anything the account owns; anything else — adopting a node,
// minting a master token or an SD bundle, any API write, inviting a member —
// is refused, so an account nobody confirmed never holds a resource.
var openSignupExemptExact = map[string]struct{}{
	"GET /api/v1/users/me":                      {},
	"GET /api/v1/users/me/permissions":          {},
	"GET /api/v1/server/capabilities":           {},
	"POST /api/v1/users/me/resend-verification": {},
}

// RequireSettledOpenSignup blocks an account created through open
// registration until its owner has confirmed their address. It is composed
// into the authenticated middleware chain once, so every authenticated API
// route is covered by construction, including ones written later.
//
// Fails CLOSED when the gate cannot decide: an error other than the two
// refusals answers through the shared error path rather than letting the
// request through.
func RequireSettledOpenSignup(gate SettledAccountGate, logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if _, ok := openSignupExemptExact[req.Method+" "+req.URL.Path]; ok {
				return next(c)
			}
			// No account or user in context: the auth middleware in front of
			// this one has already refused such a request.
			accountID, err := GetAccountIDFromContext(c)
			if err != nil {
				return next(c)
			}
			userID, err := GetUserIDFromContext(c)
			if err != nil {
				return next(c)
			}
			err = gate.RequireSettledAccount(req.Context(), accountID, userID)
			switch {
			case err == nil:
				return next(c)
			case errors.Is(err, app_errors.ErrEmailVerificationRequired):
				// 403 with a machine-readable message, as the 2FA enrolment
				// gate answers: the caller IS authenticated, so 401 would send
				// the client into a pointless refresh loop.
				return results.SendError(c, http.StatusForbidden, "email_verification_required", nil)
			default:
				logger.WarnContext(req.Context(), "the open-signup gate refused a request", "account_id", accountID, "error", err)
				return results.SendInternalServerError(c, "could not confirm the account may be used", err)
			}
		}
	}
}
