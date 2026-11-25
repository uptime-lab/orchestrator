package user

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/user_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// GET /api/v1/users returns the account's member roster, and it consulted
// neither an authz action nor membership: the JWT middleware validates
// signature, expiry and type and never re-resolves membership, so a removed
// member kept reading the roster until their access token expired and a
// caller holding a token for an account they hold no binding in read it
// permanently. The sibling GET /api/v1/members answers 401 for exactly
// this case, with a comment naming this hole.
//
// Driving the route is the only honest check: Echo publishes a route's path and
// method but not its middleware chain, so nothing but a request proves the
// guard is mounted.
func TestUserRosterRequiresActiveMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enforcer *authz_service.Enforcer
	}{
		{"no membership row at all", authztest.NonMember()},
		{"membership disabled", authztest.DisabledMember()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := driveRoster(t, tc.enforcer)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET /api/v1/users answered %d for a caller who is not an active member; want 401", rec.Code)
			}
		})
	}
}

// The guard must not cost an ordinary member their roster: it asks about
// membership, not about a role, so the lowest role has to pass it. A viewer
// reaching the handler bails on its stub service — anything but 401 proves the
// middleware let them through.
func TestUserRosterAllowsAnyActiveMember(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	rec := driveRosterAs(t, authztest.ActiveMemberWithRole(accountID, userID, "viewer"), accountID, userID)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/users answered 401 for an ACTIVE viewer; the membership guard must not gate on a role")
	}
}

// emptyRosterService answers the one call the roster route makes. Its point is
// that an ALLOWED request reaches a real 200 rather than a panic, so the
// allow-case assertion is about the middleware and not about a nil dependency.
type emptyRosterService struct{ services.UserService }

func (emptyRosterService) ListByAccountID(_ context.Context, _ user_list.UserListQuery) ([]*user.User, error) {
	return nil, nil
}

func driveRoster(t *testing.T, enforcer *authz_service.Enforcer) *httptest.ResponseRecorder {
	t.Helper()
	return driveRosterAs(t, enforcer, uuid.New(), uuid.New())
}

func driveRosterAs(t *testing.T, enforcer *authz_service.Enforcer, accountID, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	// Stands in for the JWT middleware, which is exactly the layer that does
	// NOT resolve membership: it puts the ids in the context and nothing else.
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	recentAuth := func(next echo.HandlerFunc) echo.HandlerFunc { return next }

	h := &UserHandler{userService: emptyRosterService{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.RegisterRoutes(e, auth, recentAuth, enforcer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route is not registered at all — the test is checking a path that does not exist")
	}
	return rec
}
