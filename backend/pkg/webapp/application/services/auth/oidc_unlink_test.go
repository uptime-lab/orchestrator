package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

// unlinkExtRepo serves a fixed set of LIVE links and records revocations, so a
// test can state "these are the credentials this user has" in one place.
type unlinkExtRepo struct {
	repository.ExternalIdentityRepository
	live    []external_identity.ExternalIdentity
	revoked []uuid.UUID
	// revokedPair answers RevokedExists — "this IdP account was deliberately
	// unlinked", as opposed to never linked.
	revokedPair bool
}

func (s *unlinkExtRepo) ListByUserID(context.Context, uuid.UUID) ([]external_identity.ExternalIdentity, error) {
	return s.live, nil
}

func (s *unlinkExtRepo) Revoke(_ context.Context, _, identityID uuid.UUID) error {
	s.revoked = append(s.revoked, identityID)
	return nil
}

func (s *unlinkExtRepo) GetByProviderSubject(context.Context, string, string) (*external_identity.ExternalIdentity, error) {
	return nil, app_errors.ErrExternalIdentityNotFound
}

func (s *unlinkExtRepo) RevokedExists(context.Context, string, string) (bool, error) {
	return s.revokedPair, nil
}

func (s *unlinkExtRepo) Create(context.Context, *external_identity.ExternalIdentity) error {
	return nil
}

// fixedPasskeys is a passkey count stated by the test.
type fixedPasskeys int

func (n fixedPasskeys) CountByUserID(context.Context, uuid.UUID) (int64, error) {
	return int64(n), nil
}

// A federated identity must not be removable when it is the only thing that
// can start a session.
//
// The shape that matters most is the LAST case, and it is the reason this guard
// does not reuse reauth.AvailableMethods: that function counts TOTP, and TOTP
// cannot begin a session — it is presented after a first credential is already
// accepted. A passwordless user with one identity, no passkeys and TOTP
// enrolled would have been allowed to remove their only way in, and
// password_reset deliberately refuses passwordless accounts, so there would be
// no self-service way back. A user of exactly that shape is a supported state.
func TestUnlinkOIDCLastCredentialGuard(t *testing.T) {
	userID := uuid.New()
	first := uuid.New()
	second := uuid.New()

	identity := func(id uuid.UUID) external_identity.ExternalIdentity {
		return external_identity.ExternalIdentity{ID: id, UserID: userID, Provider: "https://accounts.google.com"}
	}

	cases := []struct {
		name         string
		hashedPass   string
		passkeys     fixedPasskeys
		live         []external_identity.ExternalIdentity
		remove       uuid.UUID
		wantRevoked  bool
		wantErrIsLas bool
	}{
		{
			name:         "only identity, no password, no passkeys — refused",
			passkeys:     0,
			live:         []external_identity.ExternalIdentity{identity(first)},
			remove:       first,
			wantErrIsLas: true,
		},
		{
			name:        "only identity but a password remains — allowed",
			hashedPass:  "a-bcrypt-hash",
			passkeys:    0,
			live:        []external_identity.ExternalIdentity{identity(first)},
			remove:      first,
			wantRevoked: true,
		},
		{
			name:        "only identity but a passkey remains — allowed",
			passkeys:    1,
			live:        []external_identity.ExternalIdentity{identity(first)},
			remove:      first,
			wantRevoked: true,
		},
		{
			name:        "two identities, nothing else — allowed",
			passkeys:    0,
			live:        []external_identity.ExternalIdentity{identity(first), identity(second)},
			remove:      first,
			wantRevoked: true,
		},
		{
			// Whitespace is not a password. The stored hash is compared with
			// TrimSpace for exactly this reason.
			name:         "blank-ish password does not count",
			hashedPass:   "   ",
			passkeys:     0,
			live:         []external_identity.ExternalIdentity{identity(first)},
			remove:       first,
			wantErrIsLas: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usr := user.NewUser("someone@example.com", "Someone", tc.hashedPass)
			repo := &unlinkExtRepo{live: tc.live}
			svc := &AuthService{
				userRepo:        &stubUserRepo{byID: usr},
				extIdentityRepo: repo,
				passkeys:        tc.passkeys,
				logger:          slog.Default(),
			}

			err := svc.UnlinkOIDC(context.Background(), userID, tc.remove)

			if tc.wantErrIsLas {
				if !errors.Is(err, app_errors.ErrExternalIdentityLastCredential) {
					t.Fatalf("expected ErrExternalIdentityLastCredential, got %v", err)
				}
				if len(repo.revoked) != 0 {
					t.Fatalf("refused unlink must not revoke anything, revoked %v", repo.revoked)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected the unlink to succeed, got %v", err)
			}
			if len(repo.revoked) != 1 || repo.revoked[0] != tc.remove {
				t.Fatalf("expected exactly %s revoked, got %v", tc.remove, repo.revoked)
			}
		})
	}
}

// An identity id that is not among the caller's own links must be refused, and
// with the SAME answer as a nonexistent one — otherwise the route enumerates
// which identity ids exist on other accounts.
func TestUnlinkOIDCRefusesSomeoneElsesIdentity(t *testing.T) {
	userID := uuid.New()
	usr := user.NewUser("someone@example.com", "Someone", "a-bcrypt-hash")
	repo := &unlinkExtRepo{live: []external_identity.ExternalIdentity{
		{ID: uuid.New(), UserID: userID},
	}}
	svc := &AuthService{
		userRepo:        &stubUserRepo{byID: usr},
		extIdentityRepo: repo,
		passkeys:        fixedPasskeys(0),
		logger:          slog.Default(),
	}

	err := svc.UnlinkOIDC(context.Background(), userID, uuid.New())

	if !errors.Is(err, app_errors.ErrExternalIdentityNotFound) {
		t.Fatalf("expected ErrExternalIdentityNotFound, got %v", err)
	}
	if len(repo.revoked) != 0 {
		t.Fatalf("nothing should have been revoked, got %v", repo.revoked)
	}
}

// Without a passkey counter the guard must REFUSE rather than wave the removal
// through. "I could not count your other credentials" is not a reason to delete
// one — and a struct-literal service in a test is exactly where that nil turns
// up.
func TestUnlinkOIDCFailsClosedWithoutAPasskeyCounter(t *testing.T) {
	userID := uuid.New()
	identityID := uuid.New()
	usr := user.NewUser("someone@example.com", "Someone", "a-bcrypt-hash")
	repo := &unlinkExtRepo{live: []external_identity.ExternalIdentity{
		{ID: identityID, UserID: userID},
	}}
	svc := &AuthService{
		userRepo:        &stubUserRepo{byID: usr},
		extIdentityRepo: repo,
		logger:          slog.Default(),
	}

	err := svc.UnlinkOIDC(context.Background(), userID, identityID)

	if !errors.Is(err, app_errors.ErrExternalIdentityLastCredential) {
		t.Fatalf("expected the guard to fail closed, got %v", err)
	}
	if len(repo.revoked) != 0 {
		t.Fatalf("nothing should have been revoked, got %v", repo.revoked)
	}
}

// The unlink must survive the very next sign-in.
//
// LoginOIDC resolves (provider, subject) first; that lookup cannot see a
// revoked row, so it falls through to the email arm — which links a verified
// address silently. Without an explicit revoked check that arm re-creates the
// link the user just removed, and the whole feature is decoration.
func TestLoginOIDCDoesNotSilentlyRelinkARevokedIdentity(t *testing.T) {
	localUser := user.NewUser("owner@example.com", "Owner", "a-bcrypt-hash")
	localUser.MarkEmailVerified() // the arm that would re-link requires this
	repo := &unlinkExtRepo{revokedPair: true}
	svc := &AuthService{
		userRepo:        &stubUserRepo{byEmail: localUser},
		extIdentityRepo: repo,
		logger:          slog.Default(),
	}

	_, err := svc.LoginOIDC(context.Background(), services.OIDCIdentity{
		Provider: "https://accounts.google.com",
		Subject:  "sub-revoked",
		Email:    "owner@example.com",
	}, false)

	if !errors.Is(err, app_errors.ErrOIDCAccountLinkRequired) {
		t.Fatalf("expected ErrOIDCAccountLinkRequired, got %v", err)
	}
}
