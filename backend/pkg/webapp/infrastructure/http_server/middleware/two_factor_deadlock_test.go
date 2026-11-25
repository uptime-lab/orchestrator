package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// mandatoryTwoFactor is an account that demands a factor from a user who has
// none — the exact state that produced the deadlock.
type mandatoryTwoFactor struct{}

func (mandatoryTwoFactor) AccountRequiresTwoFactor(context.Context, uuid.UUID) bool { return true }
func (mandatoryTwoFactor) UserHasTwoFactor(context.Context, uuid.UUID) bool         { return false }

// Turning on the account-wide second-factor requirement used to lock out
// everyone who had no second factor — including the super_admin who turned it
// on, with no way back.
//
// The mechanism is a deadlock, not a missing feature: enrolling needs a step-up
// ticket, the only endpoints that mint one are under
// /api/v1/users/me/reauth (and /auth/oidc/reauth for a federated user), and the
// gate blocked exactly those. So the policy demanded a factor and forbade the
// one sequence that could produce one.
//
// This test WALKS THAT SEQUENCE rather than asserting the contents of the
// exempt list. A list-contents test would have passed both before this bug and
// after it, which is the whole reason it is written this way: the property that
// matters is "a user with no factor can still complete enrolment", and only the
// path exercises it.
func TestTwoFactorMandateDoesNotDeadlockEnrolment(t *testing.T) {
	// Every step of the real recovery, in order, for both a local and a
	// federated user. Each must reach the handler while the mandate is on.
	sequence := []struct {
		step         string
		method, path string
	}{
		{"the SPA loads the signed-in user", http.MethodGet, "/api/v1/users/me"},
		{"…and their permissions", http.MethodGet, "/api/v1/users/me/permissions"},
		{"the account page lists linked identities", http.MethodGet, "/auth/oidc/identities"},
		{"which methods can confirm identity", http.MethodGet, "/api/v1/users/me/reauth/methods"},
		{"confirm with a password", http.MethodPost, "/api/v1/users/me/reauth/password"},
		{"or with a passkey", http.MethodPost, "/api/v1/users/me/reauth/passkey/begin"},
		{"a federated user confirms at the IdP", http.MethodPost, "/auth/oidc/reauth"},
		{"…and redeems the one-time code", http.MethodPost, "/auth/oidc/reauth/complete"},
		{"enrol TOTP with the ticket", http.MethodPost, "/api/v1/users/me/2fa/enroll"},
		{"confirm the enrolment", http.MethodPost, "/api/v1/users/me/2fa/confirm"},
		{"or register a passkey instead", http.MethodPost, "/api/v1/users/me/passkeys/register/begin"},
	}

	for _, tc := range sequence {
		t.Run(tc.step, func(t *testing.T) {
			if code := runThroughGate(t, tc.method, tc.path); code != http.StatusTeapot {
				t.Fatalf("%s %s was BLOCKED by the 2FA mandate (%d); a user with no factor "+
					"cannot complete enrolment, which makes the policy a lockout", tc.method, tc.path, code)
			}
		})
	}
}

// The exemptions must not become a hole. Everything that is not part of
// enrolment stays blocked, and the two that would matter most are the ones a
// prefix over /api/v1/users/me would have swept in.
func TestTwoFactorMandateStillBlocksEverythingElse(t *testing.T) {
	blocked := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodPut, "/api/v1/users/me"},
		{http.MethodPut, "/api/v1/users/me/password"},
		// Attaching a federated identity is a way to sign in that skips the
		// TOTP gate by design — precisely the requirement being declined. This
		// is what registering the OIDC group through the composed middleware
		// closes; it was reachable before.
		{http.MethodPost, "/auth/oidc/link"},
		{http.MethodDelete, "/auth/oidc/identities/00000000-0000-0000-0000-000000000001"},
	}

	for _, tc := range blocked {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if code := runThroughGate(t, tc.method, tc.path); code != http.StatusForbidden {
				t.Fatalf("%s %s reached the handler under a 2FA mandate (%d); the exemptions have become a hole",
					tc.method, tc.path, code)
			}
		})
	}
}

// runThroughGate drives one request through the gate with an account that
// mandates a factor and a user who has none, and reports what happened:
// StatusTeapot when the handler was reached, 403 when the gate refused.
func runThroughGate(t *testing.T, method, path string) int {
	t.Helper()
	e := echo.New()
	gate := RequireTwoFactorEnrollment(mandatoryTwoFactor{}, slog.Default())

	handler := func(c echo.Context) error { return c.NoContent(http.StatusTeapot) }
	// The gate reads the ids the auth middleware sets; without them it fails
	// OPEN, which would make every case in these tests pass vacuously.
	withIdentity := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", uuid.New().String())
			c.Set("accountID", uuid.New().String())
			return next(c)
		}
	}
	e.Any("/*", handler, withIdentity, gate)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}
