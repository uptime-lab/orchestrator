package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// TwoFactorPolicy answers whether an account demands a second factor and
// whether a given user has one.
type TwoFactorPolicy interface {
	AccountRequiresTwoFactor(ctx context.Context, accountID uuid.UUID) bool
	UserHasTwoFactor(ctx context.Context, userID uuid.UUID) bool
}

// twoFactorExemptPrefixes are the ENROLLMENT paths a member without a factor
// may still reach when their account mandates one.
//
// Without these the policy would be a lockout rather than a requirement: the
// user must be able to enroll. Prefix matching is safe here because every
// entry is itself an enrollment surface.
var twoFactorExemptPrefixes = []string{
	"/api/v1/users/me/2fa",
	"/api/v1/users/me/passkeys",
	"/api/v1/server/capabilities",
	// The step-up endpoints. Without this the mandate was a DEADLOCK, not a
	// requirement: enrolling a factor needs a confirmation ticket, the only
	// paths that mint one live under this prefix, and they were blocked by the
	// very policy the enrolment would satisfy. Everyone with no second factor
	// was locked out, including the super_admin who turned the policy on, with
	// no way back. One prefix covers all five endpoints — the reauth handler
	// groups them under exactly this path.
	//
	// Exempting it grants nothing by itself: a ticket proves identity, it does
	// not authorise an action, and every route that accepts one is separately
	// behind the auth middleware.
	"/api/v1/users/me/reauth",
	// The federated half of the same deadlock, and note the SHAPE — this is not
	// an `/api/v1/...` path like every other entry here, which is exactly the
	// kind of detail that makes a fix silently not work. A user whose only
	// credential is their IdP can prove themselves nowhere else, so blocking
	// this would leave them unable to enrol and unable to confirm. The prefix
	// covers /reauth and /reauth/complete and nothing else on that group.
	"/auth/oidc/reauth",
}

// twoFactorExemptExact are single endpoints the enrollment screen needs, matched
// EXACTLY and by method.
//
// A prefix of "/api/v1/users/me" would have swept in PUT /me and
// PUT /me/password too, letting someone who refuses to enroll still rewrite
// their profile and password — the opposite of what the comment claimed.
var twoFactorExemptExact = map[string]string{
	"GET /api/v1/users/me":             "",
	"GET /api/v1/users/me/permissions": "",
	// The enrolment screen is the account page, which lists linked identities.
	// Blocked, it renders "Not linked" for an account that IS linked — a read
	// answering with a lie is worse than answering with an error. A GET of the
	// caller's own identities discloses nothing they cannot already see.
	"GET /auth/oidc/identities": "",
}

// RequireTwoFactorEnrollment blocks a member of a 2FA-mandating account from
// using the API until they have enrolled a factor.
//
// This is the server-side half of the policy. The frontend also routes such a
// user to a forced-enrollment screen, but that is a convenience: without this
// middleware the policy would be advice, enforced only by a client that chooses
// to cooperate.
//
// Fails OPEN on an unresolvable request (no account or user in context) because
// those requests are rejected by the auth middleware that runs before this one;
// duplicating that rejection here would only produce a confusing error.
func RequireTwoFactorEnrollment(policy TwoFactorPolicy, logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Request().URL.Path
			for _, prefix := range twoFactorExemptPrefixes {
				if strings.HasPrefix(path, prefix) {
					return next(c)
				}
			}
			if _, ok := twoFactorExemptExact[c.Request().Method+" "+path]; ok {
				return next(c)
			}

			accountID, err := GetAccountIDFromContext(c)
			if err != nil {
				return next(c)
			}
			userID, err := GetUserIDFromContext(c)
			if err != nil {
				return next(c)
			}

			ctx := c.Request().Context()
			if !policy.AccountRequiresTwoFactor(ctx, accountID) {
				return next(c)
			}
			if policy.UserHasTwoFactor(ctx, userID) {
				return next(c)
			}

			logger.Info("blocking request until the user enrolls a second factor",
				"user_id", userID, "account_id", accountID, "path", path)
			// 403 with a machine-readable message: the caller IS authenticated,
			// so 401 would send the client into a pointless refresh loop.
			return results.SendError(c, http.StatusForbidden, "two_factor_enrollment_required", nil)
		}
	}
}
