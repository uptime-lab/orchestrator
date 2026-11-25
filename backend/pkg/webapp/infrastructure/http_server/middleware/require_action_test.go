package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// Stub repositories for building a real Enforcer without a database. Only the
// methods the enforcer's resolve path calls are implemented; any other call
// panics via the embedded nil interface, which is exactly what a unit test
// wants from an unexpected dependency.

type stubMemberRepo struct {
	repository.AccountMemberRepository
	member *account_member.AccountMember
	err    error
}

func (r *stubMemberRepo) GetByAccountAndUser(_ context.Context, _, _ uuid.UUID) (*account_member.AccountMember, error) {
	return r.member, r.err
}

type stubBindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (r *stubBindingRepo) ListByAccountAndUser(_ context.Context, _, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

type stubRoleSettingRepo struct {
	repository.AccountRoleSettingRepository
}

func (r *stubRoleSettingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

// newTestEnforcer builds a real Enforcer over the stubs. The OU repository is
// nil: none of these cases uses an OU-scoped binding, so the enforcer never
// loads the tree (and would panic if it tried, which is a useful assertion in
// itself).
func newTestEnforcer(memberRepo *stubMemberRepo, bindings []role_binding.RoleBinding) *authz_service.Enforcer {
	recorder, _ := audittest.New()
	return authz_service.NewEnforcer(
		memberRepo,
		&stubBindingRepo{bindings: bindings},
		&stubRoleSettingRepo{},
		nil,
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func activeMemberRepo() *stubMemberRepo {
	return &stubMemberRepo{member: &account_member.AccountMember{Status: account_member.StatusActive}}
}

func accountWideBinding(accountID, userID uuid.UUID, role authz.Role) role_binding.RoleBinding {
	return *role_binding.NewRoleBinding(accountID, userID, role, nil, nil)
}

// driveRequireAction sends one request through RequireAction and returns the
// error the middleware handed back to Echo (nil when the handler ran).
func driveRequireAction(t *testing.T, enforcer *authz_service.Enforcer, action authz.Action, userID, accountID string) (err error, handlerRan bool) {
	t.Helper()
	e := echo.New()
	h := RequireAction(enforcer, action)(func(c echo.Context) error {
		handlerRan = true
		return c.NoContent(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if userID != "" {
		c.Set("userID", userID)
	}
	if accountID != "" {
		c.Set("accountID", accountID)
	}
	return h(c), handlerRan
}

func httpStatusOf(t *testing.T, err error) int {
	t.Helper()
	var he *echo.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected an *echo.HTTPError, got %T: %v", err, err)
	}
	return he.Code
}

func TestRequireActionAllowsWhenTheEnforcerGrants(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	enforcer := newTestEnforcer(activeMemberRepo(),
		[]role_binding.RoleBinding{accountWideBinding(accountID, userID, authz.RoleSuperAdmin)})

	err, handlerRan := driveRequireAction(t, enforcer, authz.ActionImageManage,
		userID.String(), accountID.String())
	if err != nil {
		t.Fatalf("expected the handler to run, got %v", err)
	}
	if !handlerRan {
		t.Error("the wrapped handler never ran")
	}
}

func TestRequireActionDeniesWithForbiddenWhenTheRoleLacksTheAction(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	// A viewer may read images but image.manage is beyond every role below
	// super_admin — the enforcer answers ErrForbidden, the middleware 403.
	enforcer := newTestEnforcer(activeMemberRepo(),
		[]role_binding.RoleBinding{accountWideBinding(accountID, userID, authz.RoleViewer)})

	err, handlerRan := driveRequireAction(t, enforcer, authz.ActionImageManage,
		userID.String(), accountID.String())
	if handlerRan {
		t.Fatal("the wrapped handler ran despite the denial")
	}
	if code := httpStatusOf(t, err); code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
}

// Membership problems are 401 (re-authenticate), not 403: the caller's session
// names an account it can no longer act in.
func TestRequireActionMapsMembershipProblemsTo401(t *testing.T) {
	cases := []struct {
		name       string
		memberRepo *stubMemberRepo
	}{
		{"not a member", &stubMemberRepo{err: app_errors.ErrAccountMemberNotFound}},
		{"membership disabled", &stubMemberRepo{
			member: &account_member.AccountMember{Status: account_member.StatusDisabled},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accountID, userID := uuid.New(), uuid.New()
			enforcer := newTestEnforcer(tc.memberRepo, nil)
			err, handlerRan := driveRequireAction(t, enforcer, authz.ActionImageManage,
				userID.String(), accountID.String())
			if handlerRan {
				t.Fatal("the wrapped handler ran despite the membership problem")
			}
			if code := httpStatusOf(t, err); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}
		})
	}
}

// No auth context at all (route wired without JWTAuth, or a broken chain) must
// fail closed with 401 before the enforcer is ever consulted.
func TestRequireActionFailsClosedWithoutAnAuthContext(t *testing.T) {
	cases := []struct {
		name              string
		userID, accountID string
	}{
		{"nothing set", "", ""},
		{"user without account", uuid.NewString(), ""},
		{"account without user", "", uuid.NewString()},
		{"unparseable ids", "not-a-uuid", "also-not-a-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The member repo would panic if consulted: GetByAccountAndUser
			// returning (nil, nil) makes the enforcer dereference nil. Reaching
			// the handler or the enforcer here is a failure either way.
			enforcer := newTestEnforcer(&stubMemberRepo{}, nil)
			err, handlerRan := driveRequireAction(t, enforcer, authz.ActionImageManage,
				tc.userID, tc.accountID)
			if handlerRan {
				t.Fatal("the wrapped handler ran with no authenticated principal")
			}
			if code := httpStatusOf(t, err); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}
		})
	}
}
