package member

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

type acceptingInviteRepo struct {
	repository.AccountInviteRepository
	accepted int
}

func (r *acceptingInviteRepo) Accept(context.Context, string) (bool, error) {
	r.accepted++
	return true, nil
}

type writableMemberRepo struct{ *fakeMemberRepo }

func (r writableMemberRepo) Create(_ context.Context, m *account_member.AccountMember) error {
	r.members[m.UserID] = m
	return nil
}

type writableBindingRepo struct{ *fakeBindingRepo }

func (r writableBindingRepo) Create(_ context.Context, b *role_binding.RoleBinding) error {
	r.bindings[b.UserID] = append(r.bindings[b.UserID], *b)
	return nil
}

// An invitation lives only as long as its issuer may issue it: an active
// member with an account-wide super_admin binding. Acceptance asks again, so
// an invitation that escaped the revocation (issued while the issuer was
// being removed, or left by a role change made outside this service) mints
// nothing.
func TestAcceptanceRefusesAnInvitationWhoseIssuerMayNoLongerIssueIt(t *testing.T) {
	ouID := uuid.New()
	cases := []struct {
		name    string
		shape   func(accountID, issuer uuid.UUID, members *fakeMemberRepo, bindings *fakeBindingRepo)
		refused bool
	}{
		{"issuer still super admin", func(uuid.UUID, uuid.UUID, *fakeMemberRepo, *fakeBindingRepo) {}, false},
		{"issuer removed", func(_, issuer uuid.UUID, m *fakeMemberRepo, b *fakeBindingRepo) {
			delete(m.members, issuer)
			delete(b.bindings, issuer)
		}, true},
		{"issuer disabled", func(_, issuer uuid.UUID, m *fakeMemberRepo, _ *fakeBindingRepo) {
			m.members[issuer].Status = account_member.StatusDisabled
		}, true},
		{"issuer demoted to admin", func(accountID, issuer uuid.UUID, _ *fakeMemberRepo, b *fakeBindingRepo) {
			b.bindings[issuer] = []role_binding.RoleBinding{*role_binding.NewRoleBinding(accountID, issuer, authz.RoleAdmin, nil, nil)}
		}, true},
		{"issuer narrowed to one unit", func(accountID, issuer uuid.UUID, _ *fakeMemberRepo, b *fakeBindingRepo) {
			b.bindings[issuer] = []role_binding.RoleBinding{*role_binding.NewRoleBinding(accountID, issuer, authz.RoleAdmin, &ouID, nil)}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accountID, issuer := uuid.New(), uuid.New()
			members := &fakeMemberRepo{members: map[uuid.UUID]*account_member.AccountMember{
				issuer: account_member.NewAccountMember(accountID, issuer),
			}}
			bindings := &fakeBindingRepo{bindings: map[uuid.UUID][]role_binding.RoleBinding{
				issuer: {*role_binding.NewRoleBinding(accountID, issuer, authz.RoleSuperAdmin, nil, nil)},
			}}
			invites := &acceptingInviteRepo{}
			svc := &Service{
				memberRepo:  writableMemberRepo{members},
				bindingRepo: writableBindingRepo{bindings},
				inviteRepo:  invites,
				auditRepo:   &fakeAuditRepo{},
				logger:      slog.Default(),
			}
			tc.shape(accountID, issuer, members, bindings)

			invite, _, err := account_invite.NewAccountInvite(accountID, "new@example.com", authz.RoleSuperAdmin, nil, issuer)
			if err != nil {
				t.Fatal(err)
			}
			invitee := user.NewUser("new@example.com", "New", "hash")
			err = svc.AcceptInviteRecord(context.Background(), invitee, invite)

			if tc.refused {
				if !errors.Is(err, app_errors.ErrInviteNotValid) {
					t.Fatalf("got %v, want ErrInviteNotValid", err)
				}
				if invites.accepted != 0 {
					t.Error("the invitation was spent")
				}
				if _, ok := bindings.bindings[invitee.ID]; ok {
					t.Error("a binding was minted for the invitee")
				}
				return
			}
			if err != nil {
				t.Fatalf("a valid issuer's invitation was refused: %v", err)
			}
			if len(bindings.bindings[invitee.ID]) != 1 {
				t.Fatal("no binding minted")
			}
		})
	}
}

// The write carries the writer and the precondition to the repository, which
// asks both under the account lock.
func TestReplaceBindingsCarriesTheWriterAndThePrecondition(t *testing.T) {
	f := newMemberFixture()
	read := []uuid.UUID{uuid.New()}
	if _, err := f.svc.ReplaceBindings(context.Background(), f.auth(), f.target,
		[]BindingSpec{{Role: authz.RoleViewer}}, &read); err != nil {
		t.Fatal(err)
	}
	got := f.mutations.lastReplace
	if got.ActorID != f.caller || got.UserID != f.target {
		t.Errorf("actor/target = %s/%s, want %s/%s", got.ActorID, got.UserID, f.caller, f.target)
	}
	if got.ExpectedBindingIDs == nil || len(*got.ExpectedBindingIDs) != 1 || (*got.ExpectedBindingIDs)[0] != read[0] {
		t.Errorf("precondition not forwarded: %v", got.ExpectedBindingIDs)
	}
}
