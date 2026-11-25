package member

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// On a route that needs both super_admin and a step-up, a caller without the
// role is refused for the role at once — not first asked to confirm their
// identity and only then refused. A super_admin still meets the step-up, and
// removing oneself (leaving) is not a role question at all.
func TestTheRoleIsCheckedBeforeTheStepUp(t *testing.T) {
	caller := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	account := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
	other := "00000000-0000-0000-0000-000000000002"

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/members/invites"},
		{http.MethodPost, "/api/v1/members/invites/00000000-0000-0000-0000-000000000001/resend"},
		{http.MethodPut, "/api/v1/members/" + other + "/bindings"},
		{http.MethodPut, "/api/v1/members/" + other + "/status"},
		{http.MethodDelete, "/api/v1/members/" + other},
	}

	serve := func(t *testing.T, isSuperAdmin bool, method, path string) (status int, message string, stepUpRan bool) {
		t.Helper()
		e := echo.New()
		auth := func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Set("userID", caller.String())
				c.Set("accountID", account.String())
				return next(c)
			}
		}
		recentAuth := func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				stepUpRan = true
				return c.JSON(http.StatusForbidden, map[string]string{"code": "reauth_required"})
			}
		}
		h := &MemberHandler{superAdmin: func(_ context.Context, a commands.AuthContext) error {
			if a.UserID != caller || a.AccountID != account {
				t.Errorf("role asked for %v, want the caller", a)
			}
			if isSuperAdmin {
				return nil
			}
			return app_errors.ErrUnauthorized
		}}
		h.RegisterRoutes(e, auth, recentAuth)

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		var body struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Message, stepUpRan
	}

	for _, r := range routes {
		t.Run("viewer "+r.method+" "+r.path, func(t *testing.T) {
			status, message, stepUpRan := serve(t, false, r.method, r.path)
			if stepUpRan {
				t.Fatal("a caller without the role was asked to step up first")
			}
			if status != http.StatusForbidden || message != "super admin rights required" {
				t.Fatalf("status %d %q, want 403 \"super admin rights required\"", status, message)
			}
		})
		t.Run("super admin "+r.method+" "+r.path, func(t *testing.T) {
			if _, _, stepUpRan := serve(t, true, r.method, r.path); !stepUpRan {
				t.Fatal("a super admin skipped the step-up")
			}
		})
	}

	t.Run("removing oneself goes to the step-up without the role", func(t *testing.T) {
		if _, _, stepUpRan := serve(t, false, http.MethodDelete, "/api/v1/members/"+caller.String()); !stepUpRan {
			t.Fatal("removing oneself was refused for the role")
		}
	})
}
