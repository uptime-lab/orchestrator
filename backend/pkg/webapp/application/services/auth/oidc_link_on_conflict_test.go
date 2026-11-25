package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

// LinkOIDCWithPassword is the server side of the "an account with this email
// already exists" step: the IdP has proven the mailbox, the password proves the
// local account, and the two together link the identity and log the user in.

type stubMemberRepo struct {
	repository.AccountMemberRepository
	memberships []account_member.AccountMember
}

func (s *stubMemberRepo) ListByUserID(_ context.Context, _ uuid.UUID) ([]account_member.AccountMember, error) {
	return s.memberships, nil
}

type stubAccountRepo struct {
	repository.AccountRepository
	account *account.Account
}

func (s *stubAccountRepo) GetByID(_ context.Context, _ uuid.UUID) (*account.Account, error) {
	if s.account == nil {
		return nil, app_errors.ErrAccountNotFound
	}
	return s.account, nil
}

// linkFixture builds a service around one local password account that belongs
// to one account, so a successful link yields an immediate token pair.
type linkFixture struct {
	svc     *AuthService
	users   *stubUserRepo
	extRepo *stubExtIdentityRepo
}

func newLinkFixture(t *testing.T, email, password string) *linkFixture {
	t.Helper()
	// Cost 4 keeps bcrypt fast in tests; the production service uses 12.
	hasher := services.NewBCryptPasswordHasher(4)
	hash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing test password: %v", err)
	}
	localUser := user.NewUser(email, "Owner", hash)
	acc := account.NewAccount("Acme")
	users := &stubUserRepo{byEmail: localUser, byID: localUser}
	extRepo := &stubExtIdentityRepo{}
	return &linkFixture{
		svc: &AuthService{
			jwtConfig:       config.JWTConfig{SecretKey: "test-secret", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour},
			userRepo:        users,
			accountRepo:     &stubAccountRepo{account: acc},
			memberRepo:      &stubMemberRepo{memberships: []account_member.AccountMember{*account_member.NewAccountMember(acc.ID, localUser.ID)}},
			extIdentityRepo: extRepo,
			refreshTokens:   newFakeRefreshStore(),
			twoFactor:       noTwoFactor{},
			passkeys:        passkeyCount{},
			passwordHasher:  hasher,
			logger:          slog.Default(),
		},
		users:   users,
		extRepo: extRepo,
	}
}

func googleIdentity(email string) services.OIDCIdentity {
	return services.OIDCIdentity{
		Provider: "https://accounts.google.com",
		Subject:  "sub-123",
		Email:    email,
	}
}

func TestLinkOIDCWithPasswordLinksAndLogsIn(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")

	result, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("owner@example.com"), "correct horse battery", "")

	if err != nil {
		t.Fatalf("expected the link to succeed, got %v", err)
	}
	if result.Tokens == nil {
		t.Fatal("expected an immediate token pair for a single-account user")
	}
	if len(f.extRepo.created) != 1 {
		t.Fatalf("expected the identity to be linked, got %d link(s)", len(f.extRepo.created))
	}
}

// The link proves mailbox ownership, so the account becomes verified — which is
// what makes every later Google login for this user silent.
func TestLinkOIDCWithPasswordStampsEmailVerified(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")

	if _, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("owner@example.com"), "correct horse battery", ""); err != nil {
		t.Fatalf("link: %v", err)
	}

	if len(f.users.updated) == 0 || f.users.updated[0].EmailVerifiedAt == nil {
		t.Fatal("expected email_verified_at to be stamped on the linked account")
	}
}

func TestLinkOIDCWithPasswordRejectsAWrongPassword(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")

	_, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("owner@example.com"), "not the password", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.extRepo.created) != 0 {
		t.Fatal("a failed password must not link the identity")
	}
}

// Errors are uniform so the endpoint cannot be used to probe which emails have
// accounts.
func TestLinkOIDCWithPasswordHidesAnUnknownAccount(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = nil // no local account with this email

	_, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("stranger@example.com"), "correct horse battery", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected the same ErrInvalidCredentials as a wrong password, got %v", err)
	}
}

// A passwordless (OIDC-provisioned) account has nothing to prove with; an empty
// hash must never be treated as a match.
func TestLinkOIDCWithPasswordRejectsAPasswordlessAccount(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = user.NewUser("owner@example.com", "Owner", "")

	_, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("owner@example.com"), "", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.extRepo.created) != 0 {
		t.Fatal("a passwordless account must not be linkable without a password")
	}
}

// The link-on-conflict flow proves a PASSWORD and then opens a session and
// links a factor-free identity. For a user who enrolled a factor that is the
// same gate Login applies: refused, nothing linked, the user is told to sign
// in normally and link from settings.
func TestLinkOIDCWithPasswordRefusesAUserWithAConfirmedTOTP(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery staple")
	f.svc.twoFactor = totpEnrolled{}
	_, err := f.svc.LinkOIDCWithPassword(context.Background(), googleIdentity("owner@example.com"), "correct horse battery staple", "")
	if !errors.Is(err, app_errors.ErrOIDCLinkNeedsSecondFactor) {
		t.Fatalf("err = %v, want ErrOIDCLinkNeedsSecondFactor", err)
	}
	if len(f.extRepo.created) != 0 {
		t.Fatal("the identity was linked despite the refusal")
	}
}

func TestLinkOIDCWithPasswordRefusesAPasskeyHolderUnderAMandatoryAccount(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery staple")
	f.svc.passkeys = passkeyCount{n: 1}
	f.svc.accountRepo.(*stubAccountRepo).account.RequireTwoFactor = true
	_, err := f.svc.LinkOIDCWithPassword(context.Background(), googleIdentity("owner@example.com"), "correct horse battery staple", "")
	if !errors.Is(err, app_errors.ErrOIDCLinkNeedsSecondFactor) {
		t.Fatalf("err = %v, want ErrOIDCLinkNeedsSecondFactor", err)
	}
	if len(f.extRepo.created) != 0 {
		t.Fatal("the identity was linked despite the refusal")
	}
}

func TestLinkOIDCWithPasswordStillWorksForAPasskeyHolderOutsideMandatoryAccounts(t *testing.T) {
	f := newLinkFixture(t, "owner@example.com", "correct horse battery staple")
	f.svc.passkeys = passkeyCount{n: 1}
	result, err := f.svc.LinkOIDCWithPassword(context.Background(), googleIdentity("owner@example.com"), "correct horse battery staple", "")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if result.Tokens == nil {
		t.Fatal("expected a session")
	}
}

