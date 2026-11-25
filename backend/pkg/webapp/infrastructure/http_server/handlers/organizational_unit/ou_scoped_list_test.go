package organizational_unit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	ou_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// AllowedOUs decides what an OU-scoped member sees of the unit tree, and it was
// exercised only against the enforcer directly — nothing drove the LIST ROUTE as
// such a member and asserted the tree that comes back. That is the half where a
// filter can be dropped, inverted, or applied to the wrong field without any
// test noticing.
//
// The routes are DRIVEN, for the reason the step-up tests are: Echo publishes a
// route's method and path but not what its handler does with a permission.

// --- repository stubs: only the OU list is ever reached ---

type listOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (r *listOURepo) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return r.ous, nil
}

type (
	stubOUMutation   struct{ repository.OUMutationRepository }
	stubNodeRepo     struct{ repository.NodeRepository }
	stubSSHKeyRepo   struct{ repository.SSHKeyRepository }
	stubSecretRepo   struct{ repository.SecretRepository }
	stubAttachRepo   struct{ repository.SecretAttachmentRepository }
	stubPolicyRepo   struct{ repository.AccountPolicyRepository }
	stubImageRepo    struct{ repository.ImageRepository }
	stubBindingRepo2 struct{ repository.RoleBindingRepository }
	stubInviteRepo   struct{ repository.AccountInviteRepository }
	stubTokenRepo    struct{ repository.MasterTokenRepository }
	stubTemplateRepo struct{ repository.WorkflowTemplateRepository }
)

// --- enforcer stubs ---

type scopedMemberRepo struct {
	repository.AccountMemberRepository
}

func (r *scopedMemberRepo) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	return account_member.NewAccountMember(accountID, userID), nil
}

type scopedBindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (r *scopedBindingRepo) ListByAccountAndUser(context.Context, uuid.UUID, uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

type scopedSettingRepo struct{ repository.AccountRoleSettingRepository }

func (r *scopedSettingRepo) ListByAccountID(context.Context, uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

type scopedOUTreeRepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (r *scopedOUTreeRepo) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return r.ous, nil
}

// listTree is the fixture tree: two roots, one with a child.
//
//	root-a
//	  └── child-a
//	root-b
type listTree struct {
	rootA, childA, rootB uuid.UUID
	ous                  []organizational_unit.OrganizationalUnit
}

func newListTree(accountID uuid.UUID) listTree {
	rootA, childA, rootB := uuid.New(), uuid.New(), uuid.New()
	return listTree{
		rootA: rootA, childA: childA, rootB: rootB,
		ous: []organizational_unit.OrganizationalUnit{
			{ID: rootA, AccountID: accountID, Name: "root-a"},
			{ID: childA, AccountID: accountID, Name: "child-a", ParentID: &rootA},
			{ID: rootB, AccountID: accountID, Name: "root-b"},
		},
	}
}

// driveOUList serves GET /api/v1/organizational-units as a member holding one
// binding, and returns the unit ids in the response.
func driveOUList(t *testing.T, binding role_binding.RoleBinding, tree listTree, accountID, userID uuid.UUID) []string {
	t.Helper()

	recorder, _ := audittest.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	enforcer := authz_service.NewEnforcer(
		&scopedMemberRepo{},
		&scopedBindingRepo{bindings: []role_binding.RoleBinding{binding}},
		&scopedSettingRepo{},
		&scopedOUTreeRepo{ous: tree.ous},
		recorder,
		logger,
	)
	svc := ou_service.NewService(
		&listOURepo{ous: tree.ous},
		&stubOUMutation{}, &stubNodeRepo{}, &stubSSHKeyRepo{}, &stubSecretRepo{},
		&stubAttachRepo{}, &stubPolicyRepo{}, &stubImageRepo{}, &stubBindingRepo2{},
		&stubInviteRepo{}, &stubTokenRepo{}, &stubTemplateRepo{},
		recorder, logger,
	)
	h := NewOrganizationalUnitHandler(svc, recorder, logger, nil)

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	h.RegisterRoutes(e, auth, enforcer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizational-units", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /organizational-units = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Units []struct {
				ID string `json:"id"`
			} `json:"units"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the response: %v — %s", err, rec.Body.String())
	}
	ids := make([]string, 0, len(body.Data.Units))
	for _, ou := range body.Data.Units {
		ids = append(ids, ou.ID)
	}
	return ids
}

func contains(ids []string, id uuid.UUID) bool {
	for _, got := range ids {
		if got == id.String() {
			return true
		}
	}
	return false
}

// An account-wide reader sees the whole tree.
func TestAnAccountWideMemberSeesEveryUnit(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	tree := newListTree(accountID)

	ids := driveOUList(t, *role_binding.NewRoleBinding(accountID, userID, authz.RoleViewer, nil, nil), tree, accountID, userID)

	for _, want := range []uuid.UUID{tree.rootA, tree.childA, tree.rootB} {
		if !contains(ids, want) {
			t.Errorf("an account-wide viewer did not see unit %s", want)
		}
	}
}

// An OU-scoped member sees their own subtree — and NOT the unrelated root.
func TestAnOUScopedMemberSeesOnlyTheirSubtree(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	tree := newListTree(accountID)

	ids := driveOUList(t, *role_binding.NewRoleBinding(accountID, userID, authz.RoleViewer, &tree.rootA, nil), tree, accountID, userID)

	if !contains(ids, tree.rootA) {
		t.Error("the member's own unit is missing from the tree")
	}
	if !contains(ids, tree.childA) {
		t.Error("a child of the member's unit is missing; a binding covers its subtree")
	}
	if contains(ids, tree.rootB) {
		t.Fatal("an OU-scoped member was shown an unrelated root; this is the whole point of the filter")
	}
}

// A member scoped to a CHILD sees its parent too — read-only context, without
// which the child cannot render its own inheritance chain.
func TestAChildScopedMemberAlsoSeesItsParent(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	tree := newListTree(accountID)

	ids := driveOUList(t, *role_binding.NewRoleBinding(accountID, userID, authz.RoleViewer, &tree.childA, nil), tree, accountID, userID)

	if !contains(ids, tree.childA) {
		t.Error("the member's own unit is missing")
	}
	if !contains(ids, tree.rootA) {
		t.Error("the parent is missing; without it the child cannot show what it inherits")
	}
	if contains(ids, tree.rootB) {
		t.Error("an unrelated root leaked into a child-scoped member's tree")
	}
}
