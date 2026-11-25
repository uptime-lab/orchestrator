package member

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The last-super_admin invariant itself now lives inside
// MemberMutationRepository, where it runs in the mutation transaction under the
// account's advisory lock. What the service still owns — and what these tests
// pin down — is that every mutating path goes through the guarded repository
// and reports the invariant's verdict faithfully.

// fakeBindingRepo serves canned bindings for the caller's privilege check.
type fakeBindingRepo struct {
	repository.RoleBindingRepository
	bindings map[uuid.UUID][]role_binding.RoleBinding // by user
}

func (f *fakeBindingRepo) ListByAccountAndUser(_ context.Context, _, userID uuid.UUID) ([]role_binding.RoleBinding, error) {
	return f.bindings[userID], nil
}

type fakeMemberRepo struct {
	repository.AccountMemberRepository
	members map[uuid.UUID]*account_member.AccountMember
}

func (f *fakeMemberRepo) GetByAccountAndUser(_ context.Context, _, userID uuid.UUID) (*account_member.AccountMember, error) {
	m, ok := f.members[userID]
	if !ok {
		return nil, app_errors.ErrAccountMemberNotFound
	}
	return m, nil
}

// fakeMutationRepo records what the service asked for and can be primed to
// reject the way the real repository does when the invariant is violated.
type fakeMutationRepo struct {
	replaceCalls    int
	keepsSuperAdmin bool
	statusCalls     int
	lastStatus      account_member.MemberStatus
	removeCalls     int
	lastReplace     repository.BindingsReplace
	err             error
}

func (f *fakeMutationRepo) ReplaceBindingsGuarded(_ context.Context, _ uuid.UUID, req repository.BindingsReplace) error {
	f.replaceCalls++
	f.keepsSuperAdmin = req.KeepsSuperAdmin
	f.lastReplace = req
	return f.err
}

func (f *fakeMutationRepo) SetStatusGuarded(_ context.Context, _, _ uuid.UUID, status account_member.MemberStatus) error {
	f.statusCalls++
	f.lastStatus = status
	return f.err
}

func (f *fakeMutationRepo) RemoveGuarded(_ context.Context, _, _ uuid.UUID) error {
	f.removeCalls++
	return f.err
}

type fakeAuditRepo struct {
	repository.AuditEventRepository
	events []audit_event.AuditEvent
}

func (f *fakeAuditRepo) Create(_ context.Context, event *audit_event.AuditEvent) error {
	f.events = append(f.events, *event)
	return nil
}

type memberFixture struct {
	svc       *Service
	mutations *fakeMutationRepo
	accountID uuid.UUID
	caller    uuid.UUID
	target    uuid.UUID
}

func newMemberFixture() *memberFixture {
	accountID, caller, target := uuid.New(), uuid.New(), uuid.New()
	mutations := &fakeMutationRepo{}
	bindings := &fakeBindingRepo{bindings: map[uuid.UUID][]role_binding.RoleBinding{
		caller: {*role_binding.NewRoleBinding(accountID, caller, authz.RoleSuperAdmin, nil, nil)},
		target: {*role_binding.NewRoleBinding(accountID, target, authz.RoleSuperAdmin, nil, nil)},
	}}
	members := &fakeMemberRepo{members: map[uuid.UUID]*account_member.AccountMember{
		caller: account_member.NewAccountMember(accountID, caller),
		target: account_member.NewAccountMember(accountID, target),
	}}
	return &memberFixture{
		svc: &Service{
			memberRepo:   members,
			bindingRepo:  bindings,
			mutationRepo: mutations,
			auditRepo:    &fakeAuditRepo{},
			logger:       slog.Default(),
		},
		mutations: mutations,
		accountID: accountID,
		caller:    caller,
		target:    target,
	}
}

func (f *memberFixture) auth() commands.AuthContext {
	return commands.NewAuthContext(f.accountID, f.caller)
}

func TestReplaceBindingsReportsWhetherSuperAdminSurvives(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []BindingSpec
		keeps bool
	}{
		{"demotion to viewer", []BindingSpec{{Role: authz.RoleViewer}}, false},
		{"stays super_admin", []BindingSpec{{Role: authz.RoleSuperAdmin}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMemberFixture()

			if _, err := f.svc.ReplaceBindings(context.Background(), f.auth(), f.target, tc.specs, nil); err != nil {
				t.Fatalf("replace: %v", err)
			}
			if f.mutations.replaceCalls != 1 {
				t.Fatalf("expected exactly one guarded replace, got %d", f.mutations.replaceCalls)
			}
			if f.mutations.keepsSuperAdmin != tc.keeps {
				t.Errorf("keepsSuperAdmin = %v, want %v", f.mutations.keepsSuperAdmin, tc.keeps)
			}
		})
	}
}

func TestReplaceBindingsPropagatesLastSuperAdminRejection(t *testing.T) {
	f := newMemberFixture()
	f.mutations.err = app_errors.ErrLastSuperAdmin

	_, err := f.svc.ReplaceBindings(context.Background(), f.auth(), f.target, []BindingSpec{{Role: authz.RoleViewer}}, nil)

	if !errors.Is(err, app_errors.ErrLastSuperAdmin) {
		t.Fatalf("expected ErrLastSuperAdmin, got %v", err)
	}
}

func TestUpdateStatusGoesThroughTheGuardedRepository(t *testing.T) {
	f := newMemberFixture()

	if err := f.svc.UpdateStatus(context.Background(), f.auth(), f.target, account_member.StatusDisabled); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if f.mutations.statusCalls != 1 || f.mutations.lastStatus != account_member.StatusDisabled {
		t.Fatalf("expected one guarded disable, got %d call(s) with status %q",
			f.mutations.statusCalls, f.mutations.lastStatus)
	}
}

func TestRemoveGoesThroughTheGuardedRepository(t *testing.T) {
	f := newMemberFixture()

	if err := f.svc.Remove(context.Background(), f.auth(), f.target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Bindings and membership must be deleted atomically — the service no
	// longer issues them as two separate repository calls.
	if f.mutations.removeCalls != 1 {
		t.Fatalf("expected exactly one guarded remove, got %d", f.mutations.removeCalls)
	}
}

func TestSelfLeaveIsStillSubjectToTheInvariant(t *testing.T) {
	f := newMemberFixture()
	f.mutations.err = app_errors.ErrLastSuperAdmin

	// A super_admin leaving their own account goes through the same guard.
	err := f.svc.Remove(context.Background(), f.auth(), f.caller)

	if !errors.Is(err, app_errors.ErrLastSuperAdmin) {
		t.Fatalf("expected ErrLastSuperAdmin on self-leave, got %v", err)
	}
}

// Invite acceptance once enforced no password rule at all — not a length,
// not even non-empty — so bcrypt would hash "" and mint a usable account.
// The only floor was minLength={8} in InviteAcceptForm, i.e. the browser, on
// the path by which people actually join an invite-gated install.
//
// The check runs before the invite is looked up, so these cases need no invite
// fixture: a nil repository proves the rule rejected the input before any
// lookup could happen. That ordering is itself the point — an invite is
// single-use, and a fumbled form must not burn the link.
func TestInviteAcceptanceEnforcesThePasswordFloorBeforeSpendingTheInvite(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     error
	}{
		{"empty", "", app_errors.ErrUserPasswordRequired},
		{"one below the floor", strings.Repeat("a", user.MinPasswordLength-1), app_errors.ErrUserPasswordTooShort},
		// bcrypt cannot hash more than this; the refusal used to read
		// "password is required".
		{"one past the ceiling", strings.Repeat("a", user.MaxPasswordLength+1), app_errors.ErrUserPasswordTooLong},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{logger: slog.Default()}

			_, _, err := svc.AcceptInviteForNewUser(context.Background(), "tok_whatever", "Someone", tc.password)

			if !errors.Is(err, tc.want) {
				t.Fatalf("password %q: got %v, want %v", tc.password, err, tc.want)
			}
		})
	}
}

// countingAuditRepo answers the invite-allowance query with a fixed number, so
// the cap can be driven without writing a day of history.
type countingAuditRepo struct {
	fakeAuditRepo
	sentInWindow int64
	askedActions []string
	countErr     error
}

func (f *countingAuditRepo) CountByAccountAndActionsSince(_ context.Context, _ uuid.UUID, actions []string, _ time.Time) (int64, error) {
	f.askedActions = actions
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.sentInWindow, nil
}

// Inviting sends mail through the operator's own DKIM-aligned relay to an
// address the TENANT chooses, so the cap is not about protecting the account —
// it is about everyone else's mail.
func TestInviteIsCappedPerAccountPerDay(t *testing.T) {
	f := newMemberFixture()
	audit := &countingAuditRepo{sentInWindow: 20}
	f.svc.auditRepo = audit
	f.svc.cfg.MaxInvitesPerDay = 20

	_, err := f.svc.Invite(context.Background(), f.auth(), "someone@example.test", authz.RoleViewer, nil)
	if !errors.Is(err, app_errors.ErrInviteRateLimited) {
		t.Fatalf("expected ErrInviteRateLimited at the cap, got %v", err)
	}

	// Both a first send and a resend must be counted, or the cap is bypassed by
	// alternating between them.
	wantActions := map[string]bool{audit_event.ActionInviteCreate: true, audit_event.ActionInviteResend: true}
	if len(audit.askedActions) != 2 {
		t.Fatalf("counted %v, want both invite actions", audit.askedActions)
	}
	for _, a := range audit.askedActions {
		if !wantActions[a] {
			t.Errorf("unexpected counted action %q", a)
		}
	}
}

// The cap is counted off the AUDIT TRAIL rather than off account_invites, and
// this is why: revoking or re-sending DELETES the invite row, so a cap counted
// there is defeated by a revoke-and-retry loop that sends unlimited mail while
// the table never holds more than one row.
func TestInviteCapSurvivesRevokingEveryInvite(t *testing.T) {
	f := newMemberFixture()
	audit := &countingAuditRepo{sentInWindow: 25}
	f.svc.auditRepo = audit
	f.svc.cfg.MaxInvitesPerDay = 20
	// No pending invites exist at all — every one was revoked.
	_, err := f.svc.Invite(context.Background(), f.auth(), "someone@example.test", authz.RoleViewer, nil)
	if !errors.Is(err, app_errors.ErrInviteRateLimited) {
		t.Fatalf("an emptied invite table must not reset the cap, got %v", err)
	}
}

// Fail OPEN on a counting failure: this bounds mail volume, and refusing a
// legitimate invite because a COUNT query failed is the worse outcome.
func TestInviteIsAllowedWhenTheCounterFails(t *testing.T) {
	f := newMemberFixture()
	f.svc.auditRepo = &countingAuditRepo{countErr: errors.New("database is down")}
	f.svc.cfg.MaxInvitesPerDay = 20

	if err := f.svc.assertInviteAllowance(context.Background(), f.accountID); err != nil {
		t.Fatalf("a failed count must not refuse the invite: %v", err)
	}
}

// 0 means unlimited, the same convention as every other guard. An install
// that sets no limit must not inherit one by accident.
func TestInviteCapOfZeroIsUnlimited(t *testing.T) {
	f := newMemberFixture()
	audit := &countingAuditRepo{sentInWindow: 1_000_000}
	f.svc.auditRepo = audit
	f.svc.cfg.MaxInvitesPerDay = 0

	if err := f.svc.assertInviteAllowance(context.Background(), f.accountID); err != nil {
		t.Fatalf("an unlimited install refused an invite: %v", err)
	}
	if audit.askedActions != nil {
		t.Error("an unlimited install must not even run the count")
	}
}

// fakeInviteRepo answers GetByID with one pending invite.
type fakeInviteRepo struct {
	repository.AccountInviteRepository
	invite *account_invite.AccountInvite
}

func (f *fakeInviteRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*account_invite.AccountInvite, error) {
	return f.invite, nil
}

func (f *fakeInviteRepo) DeletePendingByEmail(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (f *fakeInviteRepo) Create(_ context.Context, invite *account_invite.AccountInvite) error {
	f.invite = invite
	return nil
}

// A resend is the one invite action that can be pointed at a SINGLE mailbox
// repeatedly, which is what a recipient experiences as abuse — the daily cap
// bounds the volume, this bounds the rate at one address.
func TestResendIsRefusedWithinTheCooldown(t *testing.T) {
	f := newMemberFixture()
	f.svc.auditRepo = &countingAuditRepo{}
	f.svc.cfg.MaxInvitesPerDay = 20

	invite, _, err := account_invite.NewAccountInvite(f.accountID, "someone@example.test", authz.RoleViewer, nil, f.caller)
	if err != nil {
		t.Fatalf("build invite: %v", err)
	}
	f.svc.inviteRepo = &fakeInviteRepo{invite: invite}

	// Just sent: the row's CreatedAt IS the last send, because a resend replaces
	// the row rather than updating it.
	_, err = f.svc.ResendInvite(context.Background(), f.auth(), invite.ID)
	if !errors.Is(err, app_errors.ErrInviteRateLimited) {
		t.Fatalf("expected the cooldown to refuse an immediate resend, got %v", err)
	}

	// Past the cooldown it is allowed again — the guard is a delay, not a
	// one-shot.
	invite.CreatedAt = time.Now().Add(-inviteResendCooldown - time.Minute)
	if _, err := f.svc.ResendInvite(context.Background(), f.auth(), invite.ID); errors.Is(err, app_errors.ErrInviteRateLimited) {
		t.Fatalf("an invite older than the cooldown must be re-sendable, got %v", err)
	}
}

func (f *fakeMemberRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_member.AccountMember, error) {
	out := make([]account_member.AccountMember, 0, len(f.members))
	for _, m := range f.members {
		out = append(out, *m)
	}
	return out, nil
}

func (f *fakeBindingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return nil, nil
}

// The JWT middleware never re-resolves membership, so the roster read must:
// a disabled or removed member holding a still-valid access token gets
// nothing, while an active one still reads the list.
func TestListRefusesARevokedCaller(t *testing.T) {
	f := newMemberFixture()

	// Disabled caller: token still valid, membership no longer active.
	f.svc.memberRepo.(*fakeMemberRepo).members[f.caller].Status = account_member.StatusDisabled
	if _, err := f.svc.List(context.Background(), f.auth()); !errors.Is(err, app_errors.ErrUnauthorized) {
		t.Fatalf("disabled caller must get ErrUnauthorized, got %v", err)
	}

	// Removed caller: membership row gone entirely.
	delete(f.svc.memberRepo.(*fakeMemberRepo).members, f.caller)
	if _, err := f.svc.List(context.Background(), f.auth()); !errors.Is(err, app_errors.ErrUnauthorized) {
		t.Fatalf("removed caller must get ErrUnauthorized, got %v", err)
	}
}

func TestListStillServesAnActiveCaller(t *testing.T) {
	f := newMemberFixture()
	// The fixture's users are unknown to the (absent) user repo, so entries are
	// skipped — the point here is only that the gate lets an active member in.
	f.svc.userRepo = &fakeUserRepo{}
	if _, err := f.svc.List(context.Background(), f.auth()); err != nil {
		t.Fatalf("active caller must pass the membership gate, got %v", err)
	}
}

type fakeUserRepo struct {
	repository.UserRepository
}

func (f *fakeUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	return nil, app_errors.ErrUserNotFound
}

// fakeInvalidator records the (account, user) pairs the service asked the
// authz cache to drop and the users it asked the WebSocket layer to cut.
type fakeInvalidator struct {
	invalidated  []uuid.UUID
	disconnected []uuid.UUID
}

func (f *fakeInvalidator) Invalidate(_ uuid.UUID, userID uuid.UUID) {
	f.invalidated = append(f.invalidated, userID)
}

func (f *fakeInvalidator) DisconnectUser(userID uuid.UUID) {
	f.disconnected = append(f.disconnected, userID)
}

// Every mutation that changes what a member may do must drop the enforcer's
// 30-second cache AND cut the member's WebSocket — the cache alone fixes the
// next request while a stream authorized once keeps flowing. Pinned per
// mutation so a new one, or a refactor of an old one, cannot drop either
// half silently: the window bites wherever a mutation path forgets to
// invalidate.
func TestMemberMutationsInvalidateAuthzAndDisconnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(f *memberFixture) error
	}{
		{"ReplaceBindings", func(f *memberFixture) error {
			_, err := f.svc.ReplaceBindings(context.Background(), f.auth(), f.target, []BindingSpec{{Role: authz.RoleViewer}}, nil)
			return err
		}},
		{"UpdateStatus", func(f *memberFixture) error {
			return f.svc.UpdateStatus(context.Background(), f.auth(), f.target, account_member.StatusDisabled)
		}},
		{"Remove", func(f *memberFixture) error {
			return f.svc.Remove(context.Background(), f.auth(), f.target)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMemberFixture()
			inv := &fakeInvalidator{}
			f.svc.SetAuthzInvalidator(inv)
			f.svc.SetSessionTerminator(inv)
			if err := tc.run(f); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(inv.invalidated) != 1 || inv.invalidated[0] != f.target {
				t.Fatalf("%s: authz cache invalidated for %v, want exactly [%v]", tc.name, inv.invalidated, f.target)
			}
			if len(inv.disconnected) != 1 || inv.disconnected[0] != f.target {
				t.Fatalf("%s: websocket disconnected for %v, want exactly [%v]", tc.name, inv.disconnected, f.target)
			}
		})
	}
}
