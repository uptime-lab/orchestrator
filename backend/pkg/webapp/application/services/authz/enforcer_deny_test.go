package authz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// Every existing enforcer test wires an account-wide admin, so authorization
// always allows. These cover the other half: the DENY decisions, which are the
// ones the enforcer exists for.

type denyMemberRepo struct {
	repository.AccountMemberRepository
	member *account_member.AccountMember
	err    error
}

func (r *denyMemberRepo) GetByAccountAndUser(_ context.Context, _, _ uuid.UUID) (*account_member.AccountMember, error) {
	return r.member, r.err
}

type denyBindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (r *denyBindingRepo) ListByAccountAndUser(_ context.Context, _, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

type denyRoleSettingRepo struct {
	repository.AccountRoleSettingRepository
}

func (r *denyRoleSettingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

type denyOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (r *denyOURepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return r.ous, nil
}

// denyEnforcer builds a real Enforcer over an ACTIVE member with the given
// bindings and OU tree, so a denial in these tests is always a matrix/scope
// decision, never a membership accident.
func denyEnforcer(bindings []role_binding.RoleBinding, ous []organizational_unit.OrganizationalUnit) *Enforcer {
	recorder, _ := audittest.New()
	return NewEnforcer(
		&denyMemberRepo{member: &account_member.AccountMember{Status: account_member.StatusActive}},
		&denyBindingRepo{bindings: bindings},
		&denyRoleSettingRepo{},
		&denyOURepo{ous: ous},
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func ouBinding(accountID, userID, ouID uuid.UUID, role authz.Role) role_binding.RoleBinding {
	return *role_binding.NewRoleBinding(accountID, userID, role, &ouID, nil)
}

func TestAuthorizeDeniesAUserWithNoBindings(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	enforcer := denyEnforcer(nil, nil)

	err := enforcer.Authorize(context.Background(), accountID, userID, authz.ActionNodeRead, nil)
	if !errors.Is(err, app_errors.ErrForbidden) {
		t.Errorf("no bindings: err = %v, want ErrForbidden", err)
	}
	if enforcer.Can(context.Background(), accountID, userID, authz.ActionNodeRead, nil) {
		t.Error("Can reported true for a user with no bindings")
	}
}

// The rolesCovering rule, as amended for the Organization page:
// an account-level (nil-target) action is covered only by an ACCOUNT-WIDE
// binding — EXCEPT the catalog reads in authz.IsAnyScopeRead, which any
// binding carrying the action satisfies, because an OU-scoped member must be
// able to read the unit tree and the image/key catalogs their unit's policies
// reference. Both halves are asserted: the reads open, everything else stays
// closed.
func TestAnOUScopedBindingCoversCatalogReadsAndNothingElseAtAccountLevel(t *testing.T) {
	accountID, userID, ouID := uuid.New(), uuid.New(), uuid.New()
	ou := organizational_unit.OrganizationalUnit{ID: ouID, AccountID: accountID}
	enforcer := denyEnforcer(
		[]role_binding.RoleBinding{ouBinding(accountID, userID, ouID, authz.RoleAdmin)},
		[]organizational_unit.OrganizationalUnit{ou},
	)

	// The four catalog reads pass on the OU-scoped binding alone.
	for _, action := range []authz.Action{authz.ActionOURead, authz.ActionImageRead, authz.ActionSSHKeyRead, authz.ActionFavoriteSet} {
		if !authz.IsAnyScopeRead(action) {
			t.Fatalf("precondition broken: %s is no longer an any-scope read", action)
		}
		if err := enforcer.Authorize(context.Background(), accountID, userID, action, nil); err != nil {
			t.Errorf("OU-scoped admin, %s against a nil target: err = %v, want nil (any-scope catalog read)", action, err)
		}
	}

	// A nil-target action the role grants but the set does not list stays
	// refused: secret.read is admin-level and is NOT a catalog read (a secret
	// name is the {{secrets.<name>}} identifier — account-wide enumeration).
	if !authz.Grants(authz.RoleAdmin, authz.ActionSecretRead, nil) {
		t.Fatal("precondition broken: admin no longer grants secret.read; pick another action")
	}
	err := enforcer.Authorize(context.Background(), accountID, userID, authz.ActionSecretRead, nil)
	if !errors.Is(err, app_errors.ErrForbidden) {
		t.Errorf("OU-scoped admin, secret.read against a nil target: err = %v, want ErrForbidden", err)
	}

	// And a MUTATION never takes the any-scope branch even for the same
	// resources the reads open: image.manage / sshkey.manage stay account-wide.
	for _, action := range []authz.Action{authz.ActionImageManage, authz.ActionSSHKeyManage, authz.ActionOUManage} {
		err := enforcer.Authorize(context.Background(), accountID, userID, action, nil)
		if !errors.Is(err, app_errors.ErrForbidden) {
			t.Errorf("OU-scoped admin, %s against a nil target: err = %v, want ErrForbidden", action, err)
		}
	}
}

// AllowedOUs must stay strict for the any-scope reads: the door opens, but the
// LIST is still narrowed to the allowed subtree — allowAll only for a real
// account-wide binding.
func TestAllowedOUsStaysSubtreeScopedForCatalogReads(t *testing.T) {
	accountID, userID, ouID := uuid.New(), uuid.New(), uuid.New()
	childID := uuid.New()
	ous := []organizational_unit.OrganizationalUnit{
		{ID: ouID, AccountID: accountID},
		{ID: childID, AccountID: accountID, ParentID: &ouID},
	}
	enforcer := denyEnforcer(
		[]role_binding.RoleBinding{ouBinding(accountID, userID, ouID, authz.RoleViewer)},
		ous,
	)

	allowAll, allowed, err := enforcer.AllowedOUs(context.Background(), accountID, userID, authz.ActionOURead)
	if err != nil {
		t.Fatalf("AllowedOUs: %v", err)
	}
	if allowAll {
		t.Fatal("an OU-scoped binding must not report allowAll for ou.read")
	}
	if !allowed[ouID] || !allowed[childID] {
		t.Errorf("allowed set must contain the binding OU and its child, got %v", allowed)
	}
}

func TestAnOUScopedBindingGrantsItsOwnOUAndNotAnother(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	boundOU, otherOU := uuid.New(), uuid.New()
	childOU := uuid.New()
	ous := []organizational_unit.OrganizationalUnit{
		{ID: boundOU, AccountID: accountID},
		{ID: otherOU, AccountID: accountID},
		{ID: childOU, AccountID: accountID, ParentID: &boundOU},
	}
	enforcer := denyEnforcer(
		[]role_binding.RoleBinding{ouBinding(accountID, userID, boundOU, authz.RoleViewer)},
		ous,
	)

	cases := []struct {
		name    string
		target  uuid.UUID
		allowed bool
	}{
		{"the bound OU itself", boundOU, true},
		// The 2-level subtree rule: a binding on a root covers its children.
		{"a child of the bound OU", childOU, true},
		{"an unrelated OU", otherOU, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			err := enforcer.Authorize(context.Background(), accountID, userID, authz.ActionNodeRead, &target)
			if tc.allowed && err != nil {
				t.Errorf("err = %v, want allowed", err)
			}
			if !tc.allowed && !errors.Is(err, app_errors.ErrForbidden) {
				t.Errorf("err = %v, want ErrForbidden", err)
			}
		})
	}
}

// A binding on a CHILD must not leak upward to its parent: subtree coverage
// goes down the tree only.
func TestAChildOUBindingDoesNotCoverTheParent(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	parentOU, childOU := uuid.New(), uuid.New()
	ous := []organizational_unit.OrganizationalUnit{
		{ID: parentOU, AccountID: accountID},
		{ID: childOU, AccountID: accountID, ParentID: &parentOU},
	}
	enforcer := denyEnforcer(
		[]role_binding.RoleBinding{ouBinding(accountID, userID, childOU, authz.RoleAdmin)},
		ous,
	)

	err := enforcer.Authorize(context.Background(), accountID, userID, authz.ActionNodeRead, &parentOU)
	if !errors.Is(err, app_errors.ErrForbidden) {
		t.Errorf("child-scoped admin against the parent OU: err = %v, want ErrForbidden", err)
	}
}

func TestABindingWhoseRoleLacksTheActionIsDenied(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	// Account-wide viewer: the scope covers everything, so the denial is the
	// matrix itself refusing the action.
	enforcer := denyEnforcer(
		[]role_binding.RoleBinding{*role_binding.NewRoleBinding(accountID, userID, authz.RoleViewer, nil, nil)},
		nil,
	)

	cases := []struct {
		name   string
		action authz.Action
	}{
		{"destructive node action", authz.ActionNodeDelete},
		{"secret value read", authz.ActionSecretReadValue},
		{"account-global manage", authz.ActionImageManage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := enforcer.Authorize(context.Background(), accountID, userID, tc.action, nil)
			if !errors.Is(err, app_errors.ErrForbidden) {
				t.Errorf("viewer performing %s: err = %v, want ErrForbidden", tc.action, err)
			}
		})
	}
}

func TestAuthorizeMapsMembershipProblems(t *testing.T) {
	recorder, _ := audittest.New()
	newEnforcerWithMember := func(repo *denyMemberRepo) *Enforcer {
		return NewEnforcer(repo, &denyBindingRepo{}, &denyRoleSettingRepo{}, &denyOURepo{},
			recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	t.Run("unknown membership is ErrNotAMember", func(t *testing.T) {
		enforcer := newEnforcerWithMember(&denyMemberRepo{err: app_errors.ErrAccountMemberNotFound})
		err := enforcer.Authorize(context.Background(), uuid.New(), uuid.New(), authz.ActionNodeRead, nil)
		if !errors.Is(err, app_errors.ErrNotAMember) {
			t.Errorf("err = %v, want ErrNotAMember", err)
		}
	})

	t.Run("disabled membership is ErrMembershipDisabled", func(t *testing.T) {
		enforcer := newEnforcerWithMember(&denyMemberRepo{
			member: &account_member.AccountMember{Status: account_member.StatusDisabled},
		})
		err := enforcer.Authorize(context.Background(), uuid.New(), uuid.New(), authz.ActionNodeRead, nil)
		if !errors.Is(err, app_errors.ErrMembershipDisabled) {
			t.Errorf("err = %v, want ErrMembershipDisabled", err)
		}
	})
}

// Expired cache entries are reaped on the next cache miss, so a departed
// member's resolved bindings do not sit in memory for the life of the process.
func TestResolveEvictsExpiredCacheEntries(t *testing.T) {
	e := denyEnforcer(nil, nil)
	accountID, userID := uuid.New(), uuid.New()
	ctx := context.Background()

	// Prime the cache for one user, then age the entry past its TTL.
	_, _ = e.resolve(ctx, accountID, userID)
	e.mu.Lock()
	for k, v := range e.cache {
		v.expires = time.Now().Add(-time.Minute)
		e.cache[k] = v
	}
	primed := len(e.cache)
	e.mu.Unlock()
	if primed == 0 {
		t.Fatal("expected the resolve to prime the cache")
	}

	// A miss for a DIFFERENT user must reap the aged entry, not just add.
	otherUser := uuid.New()
	_, _ = e.resolve(ctx, accountID, otherUser)
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.cache {
		if k == cacheKey(accountID, userID) {
			t.Fatal("expired entry survived the sweep")
		}
	}
}
