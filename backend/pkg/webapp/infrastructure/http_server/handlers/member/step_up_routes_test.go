package member

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Privilege management must sit behind a step-up. Without it, an attacker
// holding a super_admin's access token could make two requests — POST
// /members/invites with role=super_admin, then the public /auth/invite-signup
// with the token from the response — and walk away with a SEPARATE identity
// carrying account-wide super_admin. The victim could then change their
// password, revoke every session and rotate 2FA and still not evict them,
// because the attacker's access no longer descended from the victim's
// credential.
//
// The routes are asserted by DRIVING them: Echo exposes paths and methods
// through e.Routes() but not the middleware chain, so the only honest check is
// to send a request and see whether the step-up middleware ran.
func TestPrivilegeManagementRoutesRequireStepUp(t *testing.T) {
	// Every route that hands out, rewrites or revokes privilege.
	mustStepUp := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/members/invites"},
		{http.MethodPost, "/api/v1/members/invites/00000000-0000-0000-0000-000000000001/resend"},
		{http.MethodPut, "/api/v1/members/00000000-0000-0000-0000-000000000002/bindings"},
		{http.MethodPut, "/api/v1/members/00000000-0000-0000-0000-000000000002/status"},
		{http.MethodDelete, "/api/v1/members/00000000-0000-0000-0000-000000000002"},
	}

	// Reads, self-service and the public invite acceptance must NOT be gated:
	// a step-up on DELETE /members/me would mean someone can be prevented from
	// leaving an account, and EnsureRecentAuth is not a privilege check.
	mustNotStepUp := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/members"},
		{http.MethodGet, "/api/v1/members/invites"},
		{http.MethodDelete, "/api/v1/members/me"},
		{http.MethodDelete, "/api/v1/members/invites/00000000-0000-0000-0000-000000000001"},
		{http.MethodPost, "/api/v1/invites/accept"},
	}

	for _, tc := range append(mustStepUp, mustNotStepUp...) {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			stepUpRan := false

			e := echo.New()
			// Both middlewares short-circuit, so no handler body runs and the
			// handler needs no dependencies.
			auth := func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error { return next(c) }
			}
			recentAuth := func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					stepUpRan = true
					return c.NoContent(http.StatusTeapot)
				}
			}

			h := &MemberHandler{}
			h.RegisterRoutes(e, auth, recentAuth)

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("route is not registered at all — the test is checking a path that does not exist")
			}

			want := false
			for _, g := range mustStepUp {
				if g.method == tc.method && g.path == tc.path {
					want = true
					break
				}
			}
			if stepUpRan != want {
				if want {
					t.Errorf("%s %s hands out or revokes privilege and MUST require a step-up", tc.method, tc.path)
				} else {
					t.Errorf("%s %s must NOT require a step-up", tc.method, tc.path)
				}
			}
		})
	}
}
