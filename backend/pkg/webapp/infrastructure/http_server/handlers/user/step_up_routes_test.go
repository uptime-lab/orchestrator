package user

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
)

// The routes that change how an account is signed into must sit behind a
// step-up, and the profile update is one of them even though it does not read
// like one.
//
// PUT /api/v1/users/me carries the email. The email is the sign-in identifier,
// and password reset resolves by the stored address and revokes every session
// when it completes — so a hijacked session that repoints the address takes the
// account and evicts its owner using the recovery flow itself. It sat one line
// above the password route that already had the gate.
//
// The routes are asserted by DRIVING them: Echo exposes paths and methods
// through e.Routes() but not the middleware chain, so the only honest check is
// to send a request and see whether the step-up middleware ran.
func TestCredentialRoutesRequireStepUp(t *testing.T) {
	mustStepUp := []struct{ method, path string }{
		{http.MethodPut, "/api/v1/users/me"},
		{http.MethodPut, "/api/v1/users/me/password"},
	}

	// Reads and the terms acknowledgement change no credential. Gating the
	// acknowledgement in particular would put a step-up in front of a step a
	// brand-new user meets before they have any reason to hold a ticket.
	mustNotStepUp := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodPost, "/api/v1/users/me/beta-acknowledgement"},
	}

	for _, tc := range append(mustStepUp, mustNotStepUp...) {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			stepUpRan := false

			e := echo.New()
			// The auth middleware passes through; the step-up short-circuits, so
			// no handler body runs and the handler needs no dependencies.
			auth := func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error { return next(c) }
			}
			recentAuth := func(echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					stepUpRan = true
					return c.NoContent(http.StatusTeapot)
				}
			}

			// An ungated route really reaches its handler, which bails on the
			// missing user id and logs while doing it — so the logger must exist
			// even though the service does not.
			h := &UserHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			// A non-member enforcer: the roster's membership guard then
			// short-circuits before the handler, which is irrelevant here —
			// this test asks only whether the STEP-UP ran.
			h.RegisterRoutes(e, auth, recentAuth, authztest.NonMember())

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
					t.Errorf("%s %s changes a sign-in credential and MUST require a step-up", tc.method, tc.path)
				} else {
					t.Errorf("%s %s must NOT require a step-up", tc.method, tc.path)
				}
			}
		})
	}
}
