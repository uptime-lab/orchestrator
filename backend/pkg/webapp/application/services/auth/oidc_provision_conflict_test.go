package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// heldPairExtRepo: the (provider, subject) pair is still held by a revoked
// link of somebody else, so any insert for a new user would conflict.
type heldPairExtRepo struct{ neverLinkedExtRepo }

func (*heldPairExtRepo) RevokedExists(context.Context, string, string) (bool, error) {
	return true, nil
}

// conflictingCreateExtRepo passes the pre-check and then loses the insert,
// as when another sign-in claims the pair in between.
type conflictingCreateExtRepo struct{ neverLinkedExtRepo }

func (*conflictingCreateExtRepo) Create(context.Context, *external_identity.ExternalIdentity) error {
	return app_errors.ErrExternalIdentityConflict
}

type undoMembers struct {
	repository.AccountMemberRepository
	created []account_member.AccountMember
	deleted int
}

func (m *undoMembers) Create(_ context.Context, mem *account_member.AccountMember) error {
	m.created = append(m.created, *mem)
	return nil
}

func (m *undoMembers) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	m.deleted++
	return nil
}

func (m *undoMembers) ListByUserID(context.Context, uuid.UUID) ([]account_member.AccountMember, error) {
	return m.created, nil
}

type undoBindings struct {
	repository.RoleBindingRepository
	deleted int
}

func (*undoBindings) Create(context.Context, *role_binding.RoleBinding) error { return nil }

func (b *undoBindings) DeleteByAccountAndUser(context.Context, uuid.UUID, uuid.UUID) error {
	b.deleted++
	return nil
}

type undoAccounts struct {
	repository.AccountRepository
	created, deleted int
}

func (a *undoAccounts) CreateAdmitted(context.Context, *account.Account, time.Time, repository.AccountAdmission) error {
	a.created++
	return nil
}

func (a *undoAccounts) Delete(context.Context, uuid.UUID) error {
	a.deleted++
	return nil
}

var heldIdentity = services.OIDCIdentity{
	Provider: "https://accounts.google.com", Subject: "sub-held", Email: "invitee@example.com",
}

// An identity still held by another user's revoked link cannot be linked to
// anyone new. The sign-in used to create the invited user and spend the
// invite first, then fail the link, leaving a member with no way to sign in;
// it now refuses before creating anything.
func TestOIDCInviteOnboardingRefusesAHeldIdentityBeforeCreatingAnything(t *testing.T) {
	acceptor := &refusingAcceptor{}
	svc, users, _, _ := newInviteFlowService(acceptor)
	svc.extIdentityRepo = &heldPairExtRepo{}

	_, err := svc.LoginOIDC(context.Background(), heldIdentity, false)

	if !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("LoginOIDC = %v, want ErrExternalIdentityConflict", err)
	}
	if len(users.created) != 0 || acceptor.calls != 0 {
		t.Fatalf("created %d users and accepted the invite %d times for a refused sign-in", len(users.created), acceptor.calls)
	}
}

// A link insert lost after the user was created undoes that user, its
// membership and its bindings.
func TestOIDCInviteOnboardingUndoesTheUserWhenTheLinkFails(t *testing.T) {
	svc, users, _, _ := newInviteFlowService(&refusingAcceptor{})
	svc.extIdentityRepo = &conflictingCreateExtRepo{}
	members, bindings := &undoMembers{}, &undoBindings{}
	svc.memberRepo, svc.bindingRepo = members, bindings

	_, err := svc.LoginOIDC(context.Background(), heldIdentity, false)

	if !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("LoginOIDC = %v, want ErrExternalIdentityConflict", err)
	}
	if len(users.created) != 1 || len(users.deleted) != 1 || users.created[0] != users.deleted[0] {
		t.Fatalf("the user left without a credential was not removed: created %v, deleted %v", users.created, users.deleted)
	}
	if members.deleted != 1 || bindings.deleted != 1 {
		t.Fatalf("membership deletes %d, binding deletes %d, want 1 each", members.deleted, bindings.deleted)
	}
}

// Auto-provisioning refuses a held identity before creating the account, and
// undoes the owner and the account when the link is lost afterwards.
func TestOIDCAutoProvisionIsAllOrNothing(t *testing.T) {
	newSvc := func(ext repository.ExternalIdentityRepository) (*AuthService, *inviteFlowUsers, *undoAccounts) {
		svc, users, invites, _ := newInviteFlowService(&refusingAcceptor{})
		invites.invite = nil
		svc.inviteRepo = noPendingInvites{}
		accounts := &undoAccounts{}
		svc.accountRepo = accounts
		svc.memberRepo = &undoMembers{}
		svc.bindingRepo = &undoBindings{}
		svc.extIdentityRepo = ext
		svc.userRepo = provisionUsers{users}
		return svc, users, accounts
	}

	svc, users, accounts := newSvc(&heldPairExtRepo{})
	if _, err := svc.LoginOIDC(context.Background(), heldIdentity, true); !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("LoginOIDC = %v, want ErrExternalIdentityConflict", err)
	}
	if accounts.created != 0 || len(users.created) != 0 {
		t.Fatal("an account or a user was created for a held identity")
	}

	svc, users, accounts = newSvc(&conflictingCreateExtRepo{})
	if _, err := svc.LoginOIDC(context.Background(), heldIdentity, true); !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("LoginOIDC = %v, want ErrExternalIdentityConflict", err)
	}
	if accounts.created != 1 || accounts.deleted != 1 || len(users.deleted) != 1 {
		t.Fatalf("accounts created %d deleted %d, users deleted %d; want the owner and account undone",
			accounts.created, accounts.deleted, len(users.deleted))
	}
}

type noPendingInvites struct {
	repository.AccountInviteRepository
}

func (noPendingInvites) GetPendingByEmail(context.Context, string) (*account_invite.AccountInvite, error) {
	return nil, app_errors.ErrInviteNotValid
}

type provisionUsers struct{ *inviteFlowUsers }

func (provisionUsers) MarkEmailVerified(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return true, nil
}
