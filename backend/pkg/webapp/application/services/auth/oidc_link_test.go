package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Stubs embed their interface: only the methods a test exercises are
// implemented; an unexpected call panics via the nil embedded interface.

type stubUserRepo struct {
	repository.UserRepository
	byEmail *user.User
	byID    *user.User
	updated []*user.User
}

func (s *stubUserRepo) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	if s.byEmail == nil {
		return nil, app_errors.ErrUserNotFound
	}
	return s.byEmail, nil
}

func (s *stubUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	if s.byID == nil {
		return nil, app_errors.ErrUserNotFound
	}
	return s.byID, nil
}

func (s *stubUserRepo) Update(ctx context.Context, u *user.User) error {
	s.updated = append(s.updated, u)
	return nil
}

type stubExtIdentityRepo struct {
	repository.ExternalIdentityRepository
	created []external_identity.ExternalIdentity
}

func (s *stubExtIdentityRepo) GetByProviderSubject(ctx context.Context, provider, subject string) (*external_identity.ExternalIdentity, error) {
	return nil, errors.New("not found")
}

func (s *stubExtIdentityRepo) Create(ctx context.Context, link *external_identity.ExternalIdentity) error {
	s.created = append(s.created, *link)
	return nil
}

// A Google login matching a local password account whose mailbox ownership
// was never proven must NOT silently take the account over.
func TestLoginOIDCRefusesUnverifiedLocalAccount(t *testing.T) {
	localUser := user.NewUser("victim@example.com", "Victim", "some-password-hash")
	extRepo := &stubExtIdentityRepo{}
	svc := &AuthService{
		userRepo:        &stubUserRepo{byEmail: localUser},
		extIdentityRepo: extRepo,
		logger:          slog.Default(),
	}

	_, err := svc.LoginOIDC(context.Background(), services.OIDCIdentity{
		Provider: "https://accounts.google.com",
		Subject:  "sub-123",
		Email:    "victim@example.com",
	}, false)

	if !errors.Is(err, app_errors.ErrOIDCAccountLinkRequired) {
		t.Fatalf("expected ErrOIDCAccountLinkRequired, got %v", err)
	}
	if len(extRepo.created) != 0 {
		t.Fatal("no identity link must be created for an unverified local account")
	}
}

// An explicit authenticated link whose IdP-verified email matches the local
// account's email proves mailbox ownership and stamps email_verified_at.
func TestLinkOIDCStampsVerificationOnMatchingEmail(t *testing.T) {
	localUser := user.NewUser("owner@example.com", "Owner", "hash")
	userRepo := &stubUserRepo{byID: localUser}
	svc := &AuthService{
		userRepo:        userRepo,
		extIdentityRepo: &stubExtIdentityRepo{},
		logger:          slog.Default(),
	}

	if err := svc.LinkOIDC(context.Background(), localUser.ID, services.OIDCIdentity{
		Provider: "https://accounts.google.com",
		Subject:  "sub-456",
		Email:    "Owner@Example.com", // matches after normalization
	}); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	if localUser.EmailVerifiedAt == nil {
		t.Fatal("expected email_verified_at to be stamped")
	}
	if len(userRepo.updated) != 1 {
		t.Fatalf("expected one user update, got %d", len(userRepo.updated))
	}
}

// A link whose IdP email does NOT match the local email must not stamp
// verification (it proves ownership of a different mailbox).
func TestLinkOIDCDoesNotStampOnMismatchedEmail(t *testing.T) {
	localUser := user.NewUser("owner@example.com", "Owner", "hash")
	userRepo := &stubUserRepo{byID: localUser}
	svc := &AuthService{
		userRepo:        userRepo,
		extIdentityRepo: &stubExtIdentityRepo{},
		logger:          slog.Default(),
	}

	if err := svc.LinkOIDC(context.Background(), localUser.ID, services.OIDCIdentity{
		Provider: "https://accounts.google.com",
		Subject:  "sub-789",
		Email:    "other@example.com",
	}); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	if localUser.EmailVerifiedAt != nil {
		t.Fatal("verification must not be stamped for a mismatched email")
	}
	if len(userRepo.updated) != 0 {
		t.Fatal("no user update expected for a mismatched email")
	}
}

// MarkEmailVerified is the column-specific stamp the OIDC link uses;
// recorded like Update was.
func (s *stubUserRepo) MarkEmailVerified(_ context.Context, id uuid.UUID, email string, at time.Time) (bool, error) {
	for _, u := range []*user.User{s.byID, s.byEmail} {
		if u != nil && u.ID == id {
			if u.EmailVerifiedAt != nil || user.NormalizeEmail(u.Email) != user.NormalizeEmail(email) {
				return false, nil
			}
			u.EmailVerifiedAt = &at
			s.updated = append(s.updated, u)
			return true, nil
		}
	}
	return false, nil
}

// CreateWhileSessionLive records the session it was asked to hold the insert
// under; the live test itself belongs to the SQL
// repository and its integration test.
func (s *stubExtIdentityRepo) CreateWhileSessionLive(ctx context.Context, link *external_identity.ExternalIdentity, familyID uuid.UUID, issuedAt time.Time, _ int64) error {
	if familyID == uuid.Nil || issuedAt.IsZero() {
		return app_errors.ErrTokenInvalid
	}
	return s.Create(ctx, link)
}
