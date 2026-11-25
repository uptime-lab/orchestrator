package middleware

import (
	"errors"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// RequireAction returns Echo middleware that enforces an account-level action
// (nil target OU) before the handler runs. Use it for account-global resources
// (images, SSH keys, master tokens, provisioning, account settings, OU tree)
// whose permission does not depend on a per-resource OU.
func RequireAction(enforcer *authz_service.Enforcer, action authz.Action) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := Authorize(c, enforcer, action, nil); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// RequireActiveMember gates a route on ACTIVE membership alone, with no action.
// It exists for the handful of routes whose intent really is "any active
// member may read this" (the member roster, the account role-settings map):
// without it the JWT middleware is the only gate, and that middleware never
// resolves membership, so a removed member keeps reading until their token
// expires and a member holding no binding reads forever. Any route with a
// natural action must use RequireAction instead — this is not a substitute for
// one.
func RequireActiveMember(enforcer *authz_service.Enforcer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userID, err := GetUserIDFromContext(c)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
			}
			accountID, err := GetAccountIDFromContext(c)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
			}
			if err := enforcer.RequireActiveMembership(c.Request().Context(), accountID, userID); err != nil {
				return MembershipHTTPError(err)
			}
			return next(c)
		}
	}
}

// MembershipHTTPError renders a membership/permission failure exactly once, as
// an *echo.HTTPError the caller propagates. Shared by Authorize,
// RequireActiveMember and every handler that calls the enforcer's AllowedOUs /
// PermissionMap DIRECTLY, so the wording cannot drift — and so a removed
// member is answered 401, not a 500.
func MembershipHTTPError(err error) error {
	switch {
	case errors.Is(err, app_errors.ErrForbidden):
		return echo.NewHTTPError(http.StatusForbidden, "forbidden: insufficient permissions")
	case errors.Is(err, app_errors.ErrMembershipDisabled):
		return echo.NewHTTPError(http.StatusUnauthorized, "Your membership in this account is disabled. Ask an account administrator to re-enable it.")
	case errors.Is(err, app_errors.ErrNotAMember):
		return echo.NewHTTPError(http.StatusUnauthorized, "You do not have an active account membership. Ask an account administrator for an invitation.")
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, "authorization check failed")
	}
}

// Authorize checks that the request's principal may perform action against the
// target OU (nil for account-level / OU-less resources). It returns nil when
// allowed, and a non-nil *echo.HTTPError when denied — WITHOUT writing the
// response itself. Callers MUST propagate it (`if err := Authorize(...); err
// != nil { return err }`), letting Echo's error handler render it exactly
// once. Returning nil is the ONLY "allowed" signal; a helper that both writes
// and returns nil would let handlers continue past a denial (do not do that).
//
// This is the single enforcement point shared by every mutating handler; the
// domain matrix makes the final decision. Membership problems map to 401
// (re-authenticate), permission denials to 403.
func Authorize(c echo.Context, enforcer *authz_service.Enforcer, action authz.Action, ouID *uuid.UUID) error {
	userID, err := GetUserIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	accountID, err := GetAccountIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}

	err = enforcer.Authorize(c.Request().Context(), accountID, userID, action, ouID)
	if err == nil {
		return nil
	}
	return MembershipHTTPError(err)
}
