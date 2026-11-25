package authz

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
)

// GET /api/v1/account-role-settings returns the map of where this account has
// loosened its defaults on the tunable actions — tenant data. Its own Swagger
// text has always said "any active member may read", and nothing enforced it:
// the route carried the JWT middleware alone, which never resolves membership.
//
// The route is DRIVEN because Echo publishes a route's method and path but not
// its middleware chain; only a request proves the guard is mounted.
func TestRoleSettingsReadRequiresActiveMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enforcer *authz_service.Enforcer
	}{
		{"no membership row at all", authztest.NonMember()},
		{"membership disabled", authztest.DisabledMember()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := driveRoleSettings(t, tc.enforcer, uuid.New(), uuid.New())
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET /api/v1/account-role-settings answered %d for a caller who is not an active member; want 401", rec.Code)
			}
		})
	}
}

// The guard asks about membership, not about a role: the documented contract is
// that ANY active member may read the map, so the lowest role must pass.
func TestRoleSettingsReadAllowsAnyActiveMember(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	rec := driveRoleSettings(t, authztest.ActiveMemberWithRole(accountID, userID, "viewer"), accountID, userID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/account-role-settings answered %d for an ACTIVE viewer; want 200", rec.Code)
	}
}

func driveRoleSettings(t *testing.T, enforcer *authz_service.Enforcer, accountID, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	// Stands in for the JWT middleware, which is precisely the layer that does
	// NOT resolve membership: it puts the ids in the context and nothing else.
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	recentAuth := func(next echo.HandlerFunc) echo.HandlerFunc { return next }

	h := NewAuthzHandler(enforcer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.RegisterRoutes(e, auth, recentAuth)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account-role-settings", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route is not registered at all — the test is checking a path that does not exist")
	}
	return rec
}
