package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

type fixedGate struct{ err error }

func (g fixedGate) RequireSettledAccount(context.Context, uuid.UUID, uuid.UUID) error { return g.err }

func throughOpenSignupGate(t *testing.T, gate SettledAccountGate, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	handler := func(c echo.Context) error { return c.NoContent(http.StatusTeapot) }
	// The gate reads the ids the auth middleware sets; without them it lets
	// the request through, which would make every case pass vacuously.
	withIdentity := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", uuid.New().String())
			c.Set("accountID", uuid.New().String())
			return next(c)
		}
	}
	e.Any("/*", handler, withIdentity, RequireSettledOpenSignup(gate, slog.Default()))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// An unconfirmed open sign-up can render the confirm-your-address screen and
// ask for a new link, and nothing else: no node, no token, no bundle, no
// write of any kind.
func TestOpenSignupGateAllowsOnlyTheConfirmationScreen(t *testing.T) {
	unconfirmed := fixedGate{err: app_errors.ErrEmailVerificationRequired}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/users/me/permissions"},
		{http.MethodGet, "/api/v1/server/capabilities"},
		{http.MethodPost, "/api/v1/users/me/resend-verification"},
	} {
		if rec := throughOpenSignupGate(t, unconfirmed, tc.method, tc.path); rec.Code != http.StatusTeapot {
			t.Errorf("%s %s was blocked (%d); the confirmation screen needs it", tc.method, tc.path, rec.Code)
		}
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodPost, "/api/v1/nodes"},
		{http.MethodPost, "/api/v1/master-tokens"},
		{http.MethodPost, "/api/v1/nodes/00000000-0000-0000-0000-000000000001/sd-bundle"},
		{http.MethodPost, "/api/v1/members/invites"},
		{http.MethodPut, "/api/v1/users/me"},
		{http.MethodPut, "/api/v1/users/me/password"},
		{http.MethodPost, "/api/v1/users/me/2fa/enroll"},
		{http.MethodPost, "/auth/oidc/link"},
	} {
		rec := throughOpenSignupGate(t, unconfirmed, tc.method, tc.path)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s answered %d, want 403", tc.method, tc.path, rec.Code)
			continue
		}
		var body struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Message != "email_verification_required" {
			t.Errorf("%s %s: message %q, want email_verification_required", tc.method, tc.path, body.Message)
		}
	}
}

// A settled account is not touched, and a gate that cannot decide refuses.
func TestOpenSignupGatePassesSettledAndFailsClosed(t *testing.T) {
	if rec := throughOpenSignupGate(t, fixedGate{}, http.MethodPost, "/api/v1/nodes"); rec.Code != http.StatusTeapot {
		t.Fatalf("a settled account was blocked (%d)", rec.Code)
	}
	if rec := throughOpenSignupGate(t, fixedGate{err: errors.New("database unavailable")}, http.MethodPost, "/api/v1/nodes"); rec.Code == http.StatusTeapot {
		t.Fatal("a gate error let the request through")
	}
	closed := &app_errors.RegistrationClosedError{Reason: app_errors.ReasonAccountCeiling}
	if rec := throughOpenSignupGate(t, fixedGate{err: closed}, http.MethodGet, "/api/v1/nodes"); rec.Code != http.StatusForbidden {
		t.Fatalf("a confirmed sign-up past the ceiling answered %d, want the ceiling's 403", rec.Code)
	}
}
