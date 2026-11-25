package oidc

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Attaching a federated identity must sit behind a step-up.
//
// Attaching a federated identity adds a sign-in method that survives a password
// change, a passkey rotation, a TOTP enrolment and a full session revocation,
// and an OIDC login skips the TOTP gate by design, so without a step-up an
// attacker holding one access token could link their OWN provider account and
// keep access. The parts are each defensible alone; the composition is what
// this test guards.
//
// The step-up endpoints on this same group must stay ungated: they are how a
// user whose only credential IS the IdP makes the proof /link now demands, so
// gating them would be a lockout rather than a guard. /identities is a read.
//
// The routes are asserted by DRIVING them: Echo exposes paths and methods
// through e.Routes() but not the middleware chain, so the only honest check is
// to send a request and see whether the step-up middleware ran.
func TestLinkRequiresStepUp(t *testing.T) {
	mustStepUp := []struct{ method, path string }{
		{http.MethodPost, "/auth/oidc/link"},
	}

	mustNotStepUp := []struct{ method, path string }{
		{http.MethodPost, "/auth/oidc/reauth"},
		{http.MethodPost, "/auth/oidc/reauth/complete"},
		{http.MethodGet, "/auth/oidc/identities"},
		{http.MethodGet, "/auth/oidc/config"},
		// GET /auth/oidc/start is deliberately absent: it is ungated, so the
		// handler really runs, and it dereferences the provider client this
		// zero-value handler does not have. Its gating is covered by the fact
		// that it takes no middleware at all in Register.
		{http.MethodGet, "/auth/oidc/callback"},
		{http.MethodPost, "/auth/oidc/complete"},
		{http.MethodPost, "/auth/oidc/link-existing"},
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
			recentAuth := func(echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					stepUpRan = true
					return c.NoContent(http.StatusTeapot)
				}
			}

			h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			h.Register(e, auth, recentAuth)

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
					t.Errorf("%s %s attaches a permanent sign-in method and MUST require a step-up", tc.method, tc.path)
				} else {
					t.Errorf("%s %s must NOT require a step-up — it is how a federated-only user makes one", tc.method, tc.path)
				}
			}
		})
	}
}
