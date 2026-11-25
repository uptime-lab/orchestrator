package two_factor

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The mandatory-2FA policy applies to PASSWORD users. A member whose only way
// in is a federated identity (invited by email, signed in with Google, no
// password ever set) has nothing a second factor would protect: the factor is
// never asked on that path — the IdP owns its MFA — so demanding enrolment
// only produced an authenticator nobody was ever prompted for, and a 403 on
// every request until it existed.

type stubAccountRepo struct {
	repository.AccountRepository
	acc *account.Account
}

func (r *stubAccountRepo) GetByID(_ context.Context, _ uuid.UUID) (*account.Account, error) {
	return r.acc, nil
}

// failingUserLookup stands in for a database that cannot answer.
type failingUserLookup struct{}

func (failingUserLookup) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	return nil, errors.New("database unavailable")
}

func newPolicyFixture(usr *user.User, lookup passwordLookup) (*Policy, uuid.UUID) {
	factors := NewService(&stubFactorRepo{}, &stubUserRepo{user: usr}, plainEncryptor{}, slog.Default())
	p := NewPolicy(&stubAccountRepo{acc: &account.Account{RequireTwoFactor: true}}, factors, slog.Default())
	if lookup != nil {
		p.SetPasswordLookup(lookup)
	}
	return p, usr.ID
}

func TestAPasswordlessMemberSatisfiesTheMandate(t *testing.T) {
	usr := user.NewUser("federated@example.com", "Federated", "")
	p, id := newPolicyFixture(usr, &stubUserRepo{user: usr})
	if !p.UserHasTwoFactor(context.Background(), id) {
		t.Fatal("a member with no password and no factor was blocked; the mandate is for password users")
	}
}

func TestAPasswordUserWithoutAFactorIsStillBlocked(t *testing.T) {
	usr := user.NewUser("local@example.com", "Local", "bcrypt-hash")
	p, id := newPolicyFixture(usr, &stubUserRepo{user: usr})
	if p.UserHasTwoFactor(context.Background(), id) {
		t.Fatal("a password user without a factor satisfied the mandate")
	}
}

func TestAWhitespaceOnlyHashIsNoPassword(t *testing.T) {
	usr := user.NewUser("blank@example.com", "Blank", "   ")
	p, id := newPolicyFixture(usr, &stubUserRepo{user: usr})
	if !p.UserHasTwoFactor(context.Background(), id) {
		t.Fatal("a whitespace-only hash counted as a password")
	}
}

func TestAFailedUserReadKeepsTheOldAnswer(t *testing.T) {
	// The exemption is a convenience for federated members, not a security
	// boundary; when the read fails the policy falls back to what it asked
	// before — no factor, no pass — rather than waving the request through.
	usr := user.NewUser("local@example.com", "Local", "bcrypt-hash")
	p, id := newPolicyFixture(usr, failingUserLookup{})
	if p.UserHasTwoFactor(context.Background(), id) {
		t.Fatal("a failed password lookup satisfied the mandate")
	}
}

func TestNoLookupWiredBehavesAsBefore(t *testing.T) {
	usr := user.NewUser("federated@example.com", "Federated", "")
	p, id := newPolicyFixture(usr, nil)
	if p.UserHasTwoFactor(context.Background(), id) {
		t.Fatal("without a password lookup the policy must keep its previous answer")
	}
}
