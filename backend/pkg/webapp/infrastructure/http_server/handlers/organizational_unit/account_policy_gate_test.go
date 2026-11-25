package organizational_unit

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
)

// GET /api/v1/account-policy returns the account default username, SSH key id,
// OS image id, network_first / boot_to_os / ssh_password_auth and the device and
// LED profiles. The PUT beside it requires ActionAccountPolicy and the OU-level
// siblings are gated; the GET must not rest on the JWT middleware alone, which
// never resolves membership — a removed member would read it until their token
// expired, and a caller with no binding would read it forever.
//
// It takes ou.read: the account policy is the ROOT of the same
// node → OU → account chain the unit tree carries, and ou.read is the any-scope
// catalog read that opens that tree — held by every role down to viewer
// (asserted in domain/authz), so an OU-scoped member keeps a working session.
//
// Only the DENY side is driven here. The allow side is a matrix fact
// (RoleViewer holds ou.read, pinned in domain/authz/can_test.go), and asserting
// it through this route would need a live OU service rather than a middleware.
func TestAccountPolicyReadIsGated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enforcer *authz_service.Enforcer
		want     int
	}{
		{"no membership row at all", authztest.NonMember(), http.StatusUnauthorized},
		{"membership disabled", authztest.DisabledMember(), http.StatusUnauthorized},
		{"active member holding no binding", authztest.ActiveMemberNoBindings(), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accountID, userID := uuid.New(), uuid.New()
			e := echo.New()
			// Stands in for the JWT middleware, which is exactly the layer that
			// does NOT resolve membership: it sets the ids and nothing else.
			auth := func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("userID", userID.String())
					c.Set("accountID", accountID.String())
					return next(c)
				}
			}
			recorder, _ := audittest.New()
			h := NewOrganizationalUnitHandler(nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			h.RegisterRoutes(e, auth, tc.enforcer)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/account-policy", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("route is not registered at all — the test is checking a path that does not exist")
			}
			if rec.Code != tc.want {
				t.Fatalf("GET /api/v1/account-policy answered %d for %s; want %d", rec.Code, tc.name, tc.want)
			}
		})
	}
}
