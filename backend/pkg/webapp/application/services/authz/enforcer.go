package authz

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/google/uuid"
)

// cacheTTL bounds how long resolved membership+bindings+overrides are cached,
// so a revoked or downgraded member loses access within this window without a
// DB hit on every request.
const cacheTTL = 30 * time.Second

// resolved holds a member's effective authorization inputs for one account.
type resolved struct {
	active    bool
	bindings  []role_binding.RoleBinding
	overrides authz.Override
	// ouParent maps each OU id to its parent (nil-parent roots absent), used to
	// test whether a target OU falls within an OU-scoped binding's subtree.
	ouParent map[uuid.UUID]uuid.UUID
	expires  time.Time
}

// Enforcer resolves and checks a member's permissions. It is the single place
// the HTTP middleware, WebSocket and terminal handlers consult; the domain
// matrix (authz.Grants) makes the final decision.
type Enforcer struct {
	memberRepo      repository.AccountMemberRepository
	bindingRepo     repository.RoleBindingRepository
	roleSettingRepo repository.AccountRoleSettingRepository
	ouRepo          repository.OrganizationalUnitRepository
	audit           *audit.Recorder
	logger          *slog.Logger

	mu    sync.Mutex
	cache map[string]resolved // key: accountID|userID
}

// NewEnforcer wires the enforcer. All repositories are required.
func NewEnforcer(
	memberRepo repository.AccountMemberRepository,
	bindingRepo repository.RoleBindingRepository,
	roleSettingRepo repository.AccountRoleSettingRepository,
	ouRepo repository.OrganizationalUnitRepository,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *Enforcer {
	if auditRecorder == nil {
		panic("authz.Enforcer requires an audit recorder")
	}
	return &Enforcer{
		memberRepo:      memberRepo,
		bindingRepo:     bindingRepo,
		roleSettingRepo: roleSettingRepo,
		ouRepo:          ouRepo,
		audit:           auditRecorder,
		logger:          logger.With("service", "authz"),
		cache:           make(map[string]resolved),
	}
}

// Authorize returns nil if the (account, user) principal may perform the
// action against the target OU (nil = account-level / OU-less resource). It
// returns ErrMembershipDisabled / ErrNotAMember for membership problems and
// ErrForbidden when the role does not grant the action.
func (e *Enforcer) Authorize(ctx context.Context, accountID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) error {
	r, err := e.resolve(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if !r.active {
		return app_errors.ErrMembershipDisabled
	}
	roles := r.rolesCovering(targetOU)
	// Catalog reads (authz.IsAnyScopeRead) are additionally satisfied by an
	// OU-scoped binding even against a nil target: an OU-scoped member must be
	// able to read the unit tree and the image/key catalogs their unit's
	// policies reference, or an OU-scoped grant produces a broken session.
	// List handlers still narrow WHAT is returned via AllowedOUs; this only
	// decides whether the door opens. Mutations never take this branch.
	if targetOU == nil && authz.IsAnyScopeRead(action) {
		roles = r.rolesAnyScope()
	}
	if authz.GrantsAny(roles, action, r.overrides) {
		return nil
	}
	return app_errors.ErrForbidden
}

// Can is the boolean form of Authorize (membership problems count as "no").
func (e *Enforcer) Can(ctx context.Context, accountID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) bool {
	return e.Authorize(ctx, accountID, userID, action, targetOU) == nil
}

// PermissionMap returns, per action, the scope in which the user holds it:
// "*" for account-wide, otherwise the list of OU ids (subtree-expanded). Used
// to build the frontend permission map. Actions the user cannot perform
// anywhere are omitted.
func (e *Enforcer) PermissionMap(ctx context.Context, accountID, userID uuid.UUID) (map[string]interface{}, error) {
	r, err := e.resolve(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]interface{})
	if !r.active {
		return out, nil
	}
	accountWideRoles := r.rolesCovering(nil)
	// Expand OU-scoped grants: for every binding, the OUs its scope covers.
	for _, action := range authz.AllActions() {
		if authz.GrantsAny(accountWideRoles, action, r.overrides) {
			out[string(action)] = "*"
			continue
		}
		ous := r.ousGranting(action)
		if len(ous) > 0 {
			out[string(action)] = ous
		}
	}
	return out, nil
}

// AllowedOUs reports, for an action, whether the user holds it account-wide
// (allowAll) and — if not — the set of OU ids (subtree-expanded) where they do.
// List handlers use it to filter resources by OU: account-wide → everything;
// otherwise only resources whose OU is in the set (OU-less resources are shown
// to account-wide holders only).
func (e *Enforcer) AllowedOUs(ctx context.Context, accountID, userID uuid.UUID, action authz.Action) (allowAll bool, ous map[uuid.UUID]bool, err error) {
	r, err := e.resolve(ctx, accountID, userID)
	if err != nil {
		return false, nil, err
	}
	if !r.active {
		return false, map[uuid.UUID]bool{}, nil
	}
	if authz.GrantsAny(r.rolesCovering(nil), action, r.overrides) {
		return true, nil, nil
	}
	ous = make(map[uuid.UUID]bool)
	for _, s := range r.ousGranting(action) {
		if id, perr := uuid.Parse(s); perr == nil {
			ous[id] = true
		}
	}
	return false, ous, nil
}

// RequireActiveMembership answers the question that has no action attached to
// it: is this principal still an ACTIVE member of the account? The JWT
// middleware validates signature, expiry and type and never resolves
// membership, so a route with neither an action nor this check is readable by
// a removed member until their token expires, and by a member holding no
// binding at all forever. Routes whose intent is "any active member may read
// this" (the member roster, the role-settings map) use this; every route with
// a natural action uses Authorize instead.
func (e *Enforcer) RequireActiveMembership(ctx context.Context, accountID, userID uuid.UUID) error {
	r, err := e.resolve(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if !r.active {
		return app_errors.ErrMembershipDisabled
	}
	return nil
}

// Invalidate drops the cached resolution for a member (call after a bindings /
// status change so the effect is immediate rather than up to cacheTTL later).
func (e *Enforcer) Invalidate(accountID, userID uuid.UUID) {
	e.mu.Lock()
	delete(e.cache, cacheKey(accountID, userID))
	e.mu.Unlock()
}

// resolve loads (and caches) the member's authorization inputs.
func (e *Enforcer) resolve(ctx context.Context, accountID, userID uuid.UUID) (resolved, error) {
	key := cacheKey(accountID, userID)
	now := time.Now()

	e.mu.Lock()
	if cached, ok := e.cache[key]; ok && cached.expires.After(now) {
		e.mu.Unlock()
		return cached, nil
	}
	e.mu.Unlock()

	member, err := e.memberRepo.GetByAccountAndUser(ctx, accountID, userID)
	if err != nil {
		return resolved{}, app_errors.ErrNotAMember
	}
	r := resolved{
		active:  member.Status == account_member.StatusActive,
		expires: now.Add(cacheTTL),
	}
	if r.active {
		bindings, err := e.bindingRepo.ListByAccountAndUser(ctx, accountID, userID)
		if err != nil {
			return resolved{}, err
		}
		r.bindings = bindings

		settings, err := e.roleSettingRepo.ListByAccountID(ctx, accountID)
		if err != nil {
			return resolved{}, err
		}
		ov := authz.NewOverride()
		for _, s := range settings {
			ov.Set(s.Role, authz.Action(s.Action), s.Allowed)
		}
		r.overrides = ov

		// Load the OU tree only when at least one binding is OU-scoped.
		if hasOUScopedBinding(bindings) {
			ous, err := e.ouRepo.GetAllByAccountID(ctx, accountID)
			if err != nil {
				return resolved{}, err
			}
			r.ouParent = make(map[uuid.UUID]uuid.UUID, len(ous))
			for i := range ous {
				if ous[i].ParentID != nil {
					r.ouParent[ous[i].ID] = *ous[i].ParentID
				}
			}
		}
	}

	e.mu.Lock()
	// Reap expired entries while we hold the lock anyway. The map is bounded
	// by distinct account|user pairs, but an instance serves an unbounded
	// number of those over its life — and without reaping, an entry for a
	// departed member lives exactly as long as the process does. Amortized: a sweep of N entries runs
	// at most once per cache MISS, and misses are what refill the map.
	for k, cached := range e.cache {
		if !cached.expires.After(now) {
			delete(e.cache, k)
		}
	}
	e.cache[key] = r
	e.mu.Unlock()
	return r, nil
}

// rolesCovering returns the roles whose binding scope covers the target OU.
// A nil target (account-level resource) is covered only by account-wide
// bindings. An OU target is covered by an account-wide binding OR a binding on
// that OU or its ancestor (2-level tree → the OU itself or its parent).
func (r resolved) rolesCovering(targetOU *uuid.UUID) []authz.Role {
	var roles []authz.Role
	for _, b := range r.bindings {
		if b.IsAccountWide() {
			roles = append(roles, b.Role)
			continue
		}
		if targetOU != nil && r.ouWithinBinding(*b.OUID, *targetOU) {
			roles = append(roles, b.Role)
		}
	}
	return roles
}

// rolesAnyScope returns the roles from every binding regardless of scope.
// Only the any-scope catalog-read branch in Authorize may use it.
func (r resolved) rolesAnyScope() []authz.Role {
	roles := make([]authz.Role, 0, len(r.bindings))
	for _, b := range r.bindings {
		roles = append(roles, b.Role)
	}
	return roles
}

// ousGranting returns the OU ids where the user holds the action via an
// OU-scoped binding (subtree-expanded to children).
func (r resolved) ousGranting(action authz.Action) []string {
	seen := make(map[uuid.UUID]bool)
	for _, b := range r.bindings {
		if b.IsAccountWide() || b.OUID == nil {
			continue
		}
		if !authz.Grants(b.Role, action, r.overrides) {
			continue
		}
		seen[*b.OUID] = true
		// Include children of this OU (2-level tree).
		for child, parent := range r.ouParent {
			if parent == *b.OUID {
				seen[child] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id.String())
	}
	return out
}

// ouWithinBinding reports whether targetOU is the binding OU or a child of it.
func (r resolved) ouWithinBinding(bindingOU, targetOU uuid.UUID) bool {
	if bindingOU == targetOU {
		return true
	}
	// targetOU is nested under bindingOU?
	if parent, ok := r.ouParent[targetOU]; ok && parent == bindingOU {
		return true
	}
	return false
}

func hasOUScopedBinding(bindings []role_binding.RoleBinding) bool {
	for _, b := range bindings {
		if !b.IsAccountWide() {
			return true
		}
	}
	return false
}

func cacheKey(accountID, userID uuid.UUID) string {
	return accountID.String() + "|" + userID.String()
}
