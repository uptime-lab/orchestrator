package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// neverLinkedExtRepo: no live link for any (provider, subject), none revoked,
// and every Create recorded.
type neverLinkedExtRepo struct {
	repository.ExternalIdentityRepository
	created []external_identity.ExternalIdentity
}

func (*neverLinkedExtRepo) GetByProviderSubject(context.Context, string, string) (*external_identity.ExternalIdentity, error) {
	return nil, app_errors.ErrExternalIdentityNotFound
}

func (*neverLinkedExtRepo) RevokedExists(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *neverLinkedExtRepo) Create(_ context.Context, link *external_identity.ExternalIdentity) error {
	r.created = append(r.created, *link)
	return nil
}

// A federated sign-in whose verified email matches an existing, VERIFIED
// local account must not attach the identity on its own. The account below
// enrolled TOTP; a session from the IdP alone would skip both its password
// and that factor, and the identity would outlive a password change. The
// sign-in is sent to the password proof instead, and nothing is linked.
func TestLoginOIDCNeverLinksAVerifiedAccountImplicitly(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	f.svc.twoFactor = totpEnrolled{}
	f.users.byEmail.MarkEmailVerified()
	ext := &neverLinkedExtRepo{}
	f.svc.extIdentityRepo = ext

	result, err := f.svc.LoginOIDC(context.Background(), googleIdentity("owner@example.com"), false)

	if !errors.Is(err, app_errors.ErrOIDCAccountLinkRequired) {
		t.Fatalf("LoginOIDC = (%+v, %v), want ErrOIDCAccountLinkRequired", result, err)
	}
	if result != nil {
		t.Fatal("a result was returned for an identity that is not linked")
	}
	if len(ext.created) != 0 {
		t.Fatalf("the sign-in linked %d identity(ies) to an existing account", len(ext.created))
	}
}

// A matching account with no password has nothing to prove on the spot: the
// sign-in is refused with the "sign in, then link from settings" answer.
func TestLoginOIDCRefusesAMatchingPasswordlessAccount(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "unused")
	f.users.byEmail.HashedPassword = ""
	f.users.byEmail.MarkEmailVerified()
	ext := &neverLinkedExtRepo{}
	f.svc.extIdentityRepo = ext

	_, err := f.svc.LoginOIDC(context.Background(), googleIdentity("owner@example.com"), true)

	if !errors.Is(err, app_errors.ErrOIDCAccountExists) {
		t.Fatalf("LoginOIDC = %v, want ErrOIDCAccountExists", err)
	}
	if len(ext.created) != 0 {
		t.Fatal("the sign-in linked an identity to an existing passwordless account")
	}
}

// --- Invite onboarding through the IdP --------------------------------------

type inviteFlowUsers struct {
	repository.UserRepository
	created []uuid.UUID
	deleted []uuid.UUID
}

func (*inviteFlowUsers) GetByEmail(context.Context, string) (*user.User, error) {
	return nil, app_errors.ErrUserNotFound
}

func (u *inviteFlowUsers) Create(_ context.Context, usr *user.User) error {
	u.created = append(u.created, usr.ID)
	return nil
}

func (u *inviteFlowUsers) Delete(_ context.Context, id uuid.UUID) error {
	u.deleted = append(u.deleted, id)
	return nil
}

type pendingInviteRepo struct {
	repository.AccountInviteRepository
	invite   *account_invite.AccountInvite
	accepted int
}

func (r *pendingInviteRepo) GetPendingByEmail(context.Context, string) (*account_invite.AccountInvite, error) {
	return r.invite, nil
}

func (r *pendingInviteRepo) Accept(context.Context, string) (bool, error) {
	r.accepted++
	return true, nil
}

type recordingMembers struct {
	repository.AccountMemberRepository
	created int
}

func (m *recordingMembers) Create(context.Context, *account_member.AccountMember) error {
	m.created++
	return nil
}

func (*recordingMembers) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type recordingBindings struct {
	repository.RoleBindingRepository
}

type refusingAcceptor struct {
	err   error
	calls int
}

func (a *refusingAcceptor) AcceptInviteRecord(context.Context, *user.User, *account_invite.AccountInvite) error {
	a.calls++
	return a.err
}

func newInviteFlowService(acceptor inviteAcceptor) (*AuthService, *inviteFlowUsers, *pendingInviteRepo, *neverLinkedExtRepo) {
	users := &inviteFlowUsers{}
	invites := &pendingInviteRepo{invite: &account_invite.AccountInvite{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		Email:     "invitee@example.com",
		Role:      authz.RoleViewer,
		TokenHash: "hash",
		InvitedBy: uuid.New(),
	}}
	ext := &neverLinkedExtRepo{}
	svc := &AuthService{
		userRepo:        users,
		inviteRepo:      invites,
		memberRepo:      &recordingMembers{},
		bindingRepo:     &recordingBindings{},
		extIdentityRepo: ext,
		logger:          slog.Default(),
	}
	if acceptor != nil {
		svc.SetInviteAcceptor(acceptor)
	}
	return svc, users, invites, ext
}

// Accepting an invite by signing in with the IdP goes through the same
// acceptance as the invite link, so the seat cap applies: an account at its
// cap gets no new member, the user created for the attempt is removed, and
// nothing is linked.
func TestOIDCInviteOnboardingIsHeldToTheSeatCap(t *testing.T) {
	acceptor := &refusingAcceptor{err: app_errors.ErrMemberLimitReached}
	svc, users, invites, ext := newInviteFlowService(acceptor)

	_, err := svc.LoginOIDC(context.Background(), services.OIDCIdentity{
		Provider: "https://accounts.google.com", Subject: "sub-new", Email: "invitee@example.com",
	}, false)

	if !errors.Is(err, app_errors.ErrMemberLimitReached) {
		t.Fatalf("LoginOIDC = %v, want ErrMemberLimitReached", err)
	}
	if acceptor.calls != 1 {
		t.Fatalf("the shared acceptance was asked %d times, want 1", acceptor.calls)
	}
	if invites.accepted != 0 {
		t.Fatal("the invite was flipped outside the shared acceptance")
	}
	if len(users.created) != 1 || len(users.deleted) != 1 || users.created[0] != users.deleted[0] {
		t.Fatalf("the user created for the refused attempt was not removed: created %v, deleted %v", users.created, users.deleted)
	}
	if len(ext.created) != 0 {
		t.Fatal("an identity was linked for a refused onboarding")
	}
}

// Without the shared acceptance wired the path refuses rather than accept
// with no seat check and no audit event.
func TestOIDCInviteOnboardingFailsClosedWithoutAnAcceptor(t *testing.T) {
	svc, users, invites, _ := newInviteFlowService(nil)

	_, err := svc.LoginOIDC(context.Background(), services.OIDCIdentity{
		Provider: "https://accounts.google.com", Subject: "sub-new", Email: "invitee@example.com",
	}, false)

	if !errors.Is(err, app_errors.ErrInviteNotValid) {
		t.Fatalf("LoginOIDC = %v, want ErrInviteNotValid", err)
	}
	if invites.accepted != 0 || len(users.created) != 0 {
		t.Fatal("an unchecked acceptance went ahead")
	}
}
