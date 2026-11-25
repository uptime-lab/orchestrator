// Package authztest builds a real authz.Enforcer over stub repositories, so a
// ROUTE test can assert that a guard is actually mounted on a route rather than
// that a handler happens to check something internally.
//
// A real Enforcer rather than a fake: membership resolution, the disabled-vs-
// absent distinction and the role matrix all live in it, and a stubbed one
// would let a route pass its test while the enforcement it claims never runs.
package authztest

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

type memberRepo struct {
	repository.AccountMemberRepository
	member *account_member.AccountMember
	err    error
	probe  *Probe
}

func (r *memberRepo) GetByAccountAndUser(_ context.Context, _, _ uuid.UUID) (*account_member.AccountMember, error) {
	if r.probe != nil {
		r.probe.record()
	}
	return r.member, r.err
}

// Probe counts how many times the enforcer resolved membership. Every
// Authorize / Can / AllowedOUs / PermissionMap / RequireActiveMembership call
// goes through one resolution, and a resolution that fails (the non-member
// case) is never cached, so the count is exactly the number of times a route
// consulted the enforcer. It is what lets a route test distinguish "refused
// because the enforcer said no" from "the enforcer was never asked".
type Probe struct {
	mu sync.Mutex
	n  int
}

func (p *Probe) record() {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
}

// Consulted reports how many membership resolutions ran since the last Reset.
func (p *Probe) Consulted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// Reset zeroes the count, so one enforcer can be reused across many requests.
func (p *Probe) Reset() {
	p.mu.Lock()
	p.n = 0
	p.mu.Unlock()
}

type bindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (r *bindingRepo) ListByAccountAndUser(_ context.Context, _, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

type roleSettingRepo struct {
	repository.AccountRoleSettingRepository
}

func (r *roleSettingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

type ouRepo struct {
	repository.OrganizationalUnitRepository
}

func (r *ouRepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return nil, nil
}

func build(member *account_member.AccountMember, err error, bindings []role_binding.RoleBinding) *authz_service.Enforcer {
	return buildProbed(member, err, bindings, nil)
}

func buildProbed(member *account_member.AccountMember, err error, bindings []role_binding.RoleBinding, probe *Probe) *authz_service.Enforcer {
	recorder, _ := audittest.New()
	return authz_service.NewEnforcer(
		&memberRepo{member: member, err: err, probe: probe},
		&bindingRepo{bindings: bindings},
		&roleSettingRepo{},
		&ouRepo{},
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

// NonMember resolves every principal as having no membership row at all — the
// zero-binding caller who holds a valid token for an account they were never
// in, or were removed from.
func NonMember() *authz_service.Enforcer {
	return build(nil, app_errors.ErrNotAMember, nil)
}

// NonMemberProbe is NonMember with a Probe attached: the enforcer still
// refuses every principal, and the probe records whether it was asked at all.
func NonMemberProbe() (*authz_service.Enforcer, *Probe) {
	probe := &Probe{}
	return buildProbed(nil, app_errors.ErrNotAMember, nil, probe), probe
}

// NonMemberRepository is the membership port behind NonMemberProbe, for the
// services that resolve membership THEMSELVES rather than through the enforcer
// (the member service's requireActiveMember / requireSuperAdmin). Built over
// the same probe, a resolution made there counts exactly like one made by the
// enforcer: what a route test measures is whether membership was resolved at
// all, not which object did it.
func NonMemberRepository(probe *Probe) repository.AccountMemberRepository {
	return &memberRepo{err: app_errors.ErrNotAMember, probe: probe}
}

// DisabledMember resolves every principal as a member whose status is not
// active: the removed member whose access token has not expired yet.
func DisabledMember() *authz_service.Enforcer {
	return build(&account_member.AccountMember{Status: account_member.StatusDisabled}, nil, nil)
}

// ActiveMemberNoBindings resolves every principal as an ACTIVE member holding
// no role binding at all. It is the case that separates "gated on an action"
// from "gated on membership": authentication and membership both hold, and an
// action-gated route must still refuse.
func ActiveMemberNoBindings() *authz_service.Enforcer {
	return build(&account_member.AccountMember{Status: account_member.StatusActive}, nil, nil)
}

// ActiveMemberWithRole resolves every principal as an ACTIVE member holding one
// account-wide binding for the given role, so a denial in a test using it is a
// matrix decision and never a membership accident.
func ActiveMemberWithRole(accountID, userID uuid.UUID, role authz.Role) *authz_service.Enforcer {
	binding := role_binding.NewRoleBinding(accountID, userID, role, nil, nil)
	return build(&account_member.AccountMember{Status: account_member.StatusActive}, nil, []role_binding.RoleBinding{*binding})
}

// ActiveMemberWithOURole resolves every principal as an ACTIVE member holding
// one binding for the given role scoped to ONE unit — the caller the any-scope
// door and the per-unit walls exist for. The stub OU repository holds no
// children, so the binding's subtree is the unit itself.
func ActiveMemberWithOURole(accountID, userID uuid.UUID, role authz.Role, ouID uuid.UUID) *authz_service.Enforcer {
	binding := role_binding.NewRoleBinding(accountID, userID, role, &ouID, nil)
	return build(&account_member.AccountMember{Status: account_member.StatusActive}, nil, []role_binding.RoleBinding{*binding})
}
