package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"golang.org/x/crypto/bcrypt"
)

// The credential paths answer every failure with the same body, which is worth
// nothing while the failures cost different amounts of CPU: bcrypt dominates
// the response, so a branch that returns before it runs announces itself. These
// tests assert the WORK, not the wall clock — a timing assertion would be flaky
// on a shared runner, while "did a bcrypt comparison at the production cost
// happen" is exactly the property that makes the timings equal.

// countingHasher records the hashes it was asked to compare against, and does
// the real comparison so a wrong password still fails.
type countingHasher struct {
	services.PasswordHasher
	compared []string
}

func (c *countingHasher) ComparePasswords(hashedPassword, plainPassword string) error {
	c.compared = append(c.compared, hashedPassword)
	return c.PasswordHasher.ComparePasswords(hashedPassword, plainPassword)
}

// loginTimingFixture holds one local password account, plus the hasher that
// records what the service compared.
type loginTimingFixture struct {
	svc    *AuthService
	users  *stubUserRepo
	hasher *countingHasher
}

func newLoginTimingFixture(t *testing.T, email, password string) *loginTimingFixture {
	t.Helper()
	// Cost 4 keeps the stored hash cheap; the dummy hash carries its own cost.
	hasher := &countingHasher{PasswordHasher: services.NewBCryptPasswordHasher(4)}
	hash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing test password: %v", err)
	}
	localUser := user.NewUser(email, "Owner", hash)
	users := &stubUserRepo{byEmail: localUser, byID: localUser}
	return &loginTimingFixture{
		svc: &AuthService{
			userRepo:        users,
			extIdentityRepo: &stubExtIdentityRepo{},
			twoFactor:       noTwoFactor{},
			passwordHasher:  hasher,
			logger:          slog.Default(),
		},
		users:  users,
		hasher: hasher,
	}
}

func TestDummyPasswordHashCarriesTheProductionCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	if err != nil {
		t.Fatalf("dummyPasswordHash is not a bcrypt hash: %v", err)
	}
	if cost != productionBcryptCost {
		t.Fatalf("dummy hash cost %d, production cost %d — the placeholder comparison "+
			"is itself distinguishable by timing", cost, productionBcryptCost)
	}
}

func TestLoginComparesAPasswordForAnUnknownEmail(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = nil // nobody holds this address

	_, err := f.svc.Login("stranger@example.com", "correct horse battery", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.hasher.compared) != 1 {
		t.Fatalf("expected one password comparison on the unknown-email path, got %d", len(f.hasher.compared))
	}
	if f.hasher.compared[0] != dummyPasswordHash {
		t.Fatal("the unknown-email path must compare against the dummy hash")
	}
}

func TestLoginComparesAPasswordForAPasswordlessAccount(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = user.NewUser("owner@example.com", "Owner", "") // OIDC-provisioned

	_, err := f.svc.Login("owner@example.com", "correct horse battery", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.hasher.compared) != 1 || f.hasher.compared[0] != dummyPasswordHash {
		t.Fatalf("expected the passwordless path to compare against the dummy hash, compared %q", f.hasher.compared)
	}
}

func TestLinkOIDCWithPasswordComparesAPasswordForAnUnknownEmail(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = nil

	_, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("stranger@example.com"), "correct horse battery", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.hasher.compared) != 1 || f.hasher.compared[0] != dummyPasswordHash {
		t.Fatalf("expected the unknown-email path to compare against the dummy hash, compared %q", f.hasher.compared)
	}
}

func TestLinkOIDCWithPasswordComparesAPasswordForAPasswordlessAccount(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")
	f.users.byEmail = user.NewUser("owner@example.com", "Owner", "")

	_, err := f.svc.LinkOIDCWithPassword(context.Background(),
		googleIdentity("owner@example.com"), "correct horse battery", "")

	if !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if len(f.hasher.compared) != 1 || f.hasher.compared[0] != dummyPasswordHash {
		t.Fatalf("expected the passwordless path to compare against the dummy hash, compared %q", f.hasher.compared)
	}
}

// The empty hash must never be handed to bcrypt as if it could match: bcrypt
// rejects it for being malformed, which is the right answer for the wrong
// reason and would become a match the day the hash format changed.
func TestCheckPasswordNeverMatchesAnEmptyHash(t *testing.T) {
	f := newLoginTimingFixture(t, "owner@example.com", "correct horse battery")

	if err := f.svc.checkPassword("", ""); !errors.Is(err, app_errors.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for an absent hash, got %v", err)
	}
}
