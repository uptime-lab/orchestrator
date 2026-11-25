package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// mailFake is a verifier that can (or cannot) deliver, and records sends.
type mailFake struct {
	enabled bool
	sent    []string
}

func (m *mailFake) SendFor(_ context.Context, u *user.User) error {
	m.sent = append(m.sent, u.Email)
	return nil
}
func (m *mailFake) Enabled() bool { return m.enabled }

var openPolicy = OpenRegistration{Enabled: true, MaxPending: 3, PendingWindow: 48 * time.Hour}

// openService is an instance with open registration on and working mail.
func openService(repo *countingAccountRepo, limit int, code string) (*AuthService, *mailFake) {
	s := admissionService(repo, limit, code)
	s.openRegistration = openPolicy
	mail := &mailFake{enabled: true}
	s.emailVerifier = mail
	s.runAsync = func(f func()) { f() }
	return s, mail
}

// With open registration a further organisation registers with no code at
// all, and with any code: the code is not part of the decision. The account
// it creates is marked pending.
func TestOpenRegistrationAdmitsWithoutACodeAndMarksPending(t *testing.T) {
	for _, configured := range []string{"", "the-code"} {
		s, _ := openService(&countingAccountRepo{}, 0, configured)
		for _, presented := range []string{"", "wrong", "the-code"} {
			candidate := &account.Account{}
			if err := s.admitSignup(presented)(settledCounts(3), candidate); err != nil {
				t.Errorf("configured %q, presented %q: %v, want admitted", configured, presented, err)
			}
			if candidate.OpenSignupPendingSince == nil {
				t.Errorf("configured %q, presented %q: the open sign-up was not marked pending", configured, presented)
			}
		}
	}
}

// The first owner and the invite-code path are never marked: they keep
// today's behaviour exactly.
func TestOnlyTheOpenPathMarksAnAccountPending(t *testing.T) {
	s, _ := openService(&countingAccountRepo{}, 0, "")
	first := &account.Account{}
	if err := s.admitSignup("")(repository.AccountCounts{}, first); err != nil || first.OpenSignupPendingSince != nil {
		t.Fatalf("first owner under open registration: err %v, marked %v", err, first.OpenSignupPendingSince != nil)
	}

	closedToCode := admissionService(&countingAccountRepo{}, 0, "the-code")
	viaCode := &account.Account{}
	if err := closedToCode.admitSignup("the-code")(settledCounts(3), viaCode); err != nil || viaCode.OpenSignupPendingSince != nil {
		t.Fatalf("invite-code sign-up: err %v, marked %v", err, viaCode.OpenSignupPendingSince != nil)
	}
	if err := closedToCode.admitUnderCeiling(settledCounts(3), viaCode); err != nil || viaCode.OpenSignupPendingSince != nil {
		t.Fatalf("the OIDC path's admission marked its candidate: %v", err)
	}
}

// The ceiling bounds SETTLED accounts: pending sign-ups do not spend it, so
// junk cannot close the instance to real organisations, and at the ceiling
// the refusal is the existing one.
func TestOpenRegistrationCeilingCountsSettledAccountsOnly(t *testing.T) {
	s, _ := openService(&countingAccountRepo{}, 5, "")
	busyWithJunk := repository.AccountCounts{Total: 7, Settled: 4, Pending: 2}
	if err := s.admitSignup("")(busyWithJunk, &account.Account{}); err != nil {
		t.Fatalf("pending sign-ups were counted toward the ceiling: %v", err)
	}
	full := repository.AccountCounts{Total: 5, Settled: 5}
	if err := s.admitSignup("")(full, &account.Account{}); !isClosed(err, app_errors.ReasonAccountCeiling) {
		t.Fatalf("at the ceiling: %v, want closed for the ceiling", err)
	}
}

// Pending sign-ups are themselves bounded: past the cap, "try again later".
func TestOpenRegistrationCapsPendingSignups(t *testing.T) {
	s, _ := openService(&countingAccountRepo{}, 0, "")
	full := repository.AccountCounts{Total: 4, Settled: 1, Pending: int64(openPolicy.MaxPending)}
	err := s.admitSignup("")(full, &account.Account{})
	var busy *app_errors.RegistrationBusyError
	if !errors.As(err, &busy) || busy.HTTPStatus() != 429 {
		t.Fatalf("at the pending cap: %v, want the 429 busy refusal", err)
	}
	below := repository.AccountCounts{Total: 3, Settled: 1, Pending: int64(openPolicy.MaxPending - 1)}
	if err := s.admitSignup("")(below, &account.Account{}); err != nil {
		t.Fatalf("below the pending cap: %v", err)
	}
}

// Without working mail nobody could ever confirm an open sign-up, so it is
// closed, and the sign-up page is told so.
func TestOpenRegistrationWithoutMailIsClosed(t *testing.T) {
	repo := &countingAccountRepo{accounts: 2}
	s, mail := openService(repo, 0, "the-code")
	mail.enabled = false
	if err := admitWith(s.admitSignup(""), 2); !isClosed(err, app_errors.ReasonOpenWithoutMail) {
		t.Fatalf("open without mail: %v, want closed", err)
	}
	if st, err := s.RegistrationState(context.Background()); err != nil || st != "closed" {
		t.Fatalf("RegistrationState = %q, %v; want closed", st, err)
	}
	s.emailVerifier = nil
	if err := admitWith(s.admitSignup(""), 2); !isClosed(err, app_errors.ReasonOpenWithoutMail) {
		t.Fatalf("open with no verifier at all: %v, want closed", err)
	}
}

// Off is the default, and off keeps an empty code meaning CLOSED.
func TestOpenRegistrationOffKeepsAnEmptyCodeClosed(t *testing.T) {
	s := admissionService(&countingAccountRepo{}, 0, "")
	if err := admitWith(s.admitSignup(""), 1); !isClosed(err, app_errors.ReasonNoInviteCode) {
		t.Fatalf("no code, open registration off: %v, want closed", err)
	}
}

// signupService is an open instance whose writes succeed, for Register.
func signupService(users repository.UserRepository) (*AuthService, *countingAccountRepo, *mailFake) {
	repo := &countingAccountRepo{accounts: 2, allowCreate: true}
	s, mail := openService(repo, 0, "")
	s.userRepo = users
	s.memberRepo = registrationMembers{}
	s.bindingRepo = registrationBindings{}
	s.passwordHasher = fastHasher{}
	return s, repo, mail
}

type fastHasher struct{}

func (fastHasher) HashPassword(string) (string, error)   { return "hash", nil }
func (fastHasher) ComparePasswords(string, string) error { return errors.New("no") }

// freshUsers knows no address and accepts the new user.
type freshUsers struct {
	repository.UserRepository
	lookups int
}

func (u *freshUsers) GetByEmail(context.Context, string) (*user.User, error) {
	u.lookups++
	return nil, app_errors.ErrUserNotFound
}
func (u *freshUsers) Create(context.Context, *user.User) error { return nil }

// An open sign-up gets no session: the account is created pending and the
// confirmation link is mailed.
func TestOpenSignupAnswersPendingWithoutASession(t *testing.T) {
	users := &freshUsers{}
	s, repo, mail := signupService(users)

	got, err := s.Register(user_register.RegisterCommand{Email: "new@example.com", Password: "long-enough-pw", Name: "New"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !got.VerificationPending || got.Tokens != nil || got.User != nil {
		t.Fatalf("Register = %+v, want pending with no session", got)
	}
	if repo.written == nil || repo.written.OpenSignupPendingSince == nil {
		t.Fatal("the account was not written as a pending open sign-up")
	}
	if len(mail.sent) != 1 || mail.sent[0] != "new@example.com" {
		t.Fatalf("confirmation mail sent to %v, want the new address once", mail.sent)
	}
}

// On the open path an address that already has an account gets the fresh
// sign-up's answer, nothing is created, and nothing is mailed: the form must
// not tell a stranger which addresses are registered.
func TestOpenSignupDoesNotRevealARegisteredAddress(t *testing.T) {
	users := &emailProbe{}
	s, repo, mail := signupService(users)

	got, err := s.Register(user_register.RegisterCommand{Email: "registered@example.com", Password: "long-enough-pw"})
	if err != nil {
		t.Fatalf("Register = %v, want the pending answer (not 409)", err)
	}
	if !got.VerificationPending || got.Tokens != nil {
		t.Fatalf("Register = %+v, want pending with no session", got)
	}
	if repo.created != 0 {
		t.Fatal("an account was created for an address that already had one")
	}
	if len(mail.sent) != 0 {
		t.Fatalf("mail was sent for a registered address: %v", mail.sent)
	}
	if users.lookups != 1 {
		t.Fatalf("address looked up %d times, want 1 (after admission)", users.lookups)
	}
}

// The pending cap is decided before the address is looked up, so a full cap
// answers the same for every address.
func TestOpenSignupBusyIsDecidedBeforeTheLookup(t *testing.T) {
	users := &emailProbe{}
	s, repo, _ := signupService(users)
	repo.pending = int64(openPolicy.MaxPending)
	repo.accounts = repo.pending + 1

	_, err := s.Register(user_register.RegisterCommand{Email: "registered@example.com", Password: "long-enough-pw"})
	var busy *app_errors.RegistrationBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Register = %v, want busy", err)
	}
	if users.lookups != 0 {
		t.Fatal("the address was looked up although the admission refused")
	}
}

// gateAccounts serves the open-signup gate's reads and records settles.
type gateAccounts struct {
	repository.AccountRepository
	acc       *account.Account
	readErr   error
	reads     int
	settles   int
	counts    repository.AccountCounts
	settleErr error
}

func (g *gateAccounts) GetByID(context.Context, uuid.UUID) (*account.Account, error) {
	g.reads++
	if g.readErr != nil {
		return nil, g.readErr
	}
	return g.acc, nil
}

func (g *gateAccounts) SettleOpenSignup(_ context.Context, _ uuid.UUID, _ time.Time, admit repository.AccountAdmission) (bool, error) {
	g.settles++
	if err := admit(g.counts, g.acc); err != nil {
		return false, err
	}
	g.acc.OpenSignupPendingSince = nil
	return true, nil
}

func gateService(acc *account.Account, owner *user.User, limit int) (*AuthService, *gateAccounts) {
	g := &gateAccounts{acc: acc, counts: settledCounts(1)}
	s := admissionService(&countingAccountRepo{}, limit, "")
	s.accountRepo = g
	s.userRepo = &stubUserRepo{byID: owner}
	return s, g
}

func pendingAccount() *account.Account {
	since := time.Now().Add(-time.Hour)
	acc := account.NewAccount("junk")
	acc.OpenSignupPendingSince = &since
	return acc
}

// An ordinary account passes the gate, and is read once per process.
func TestTheGateLetsAnOrdinaryAccountThrough(t *testing.T) {
	s, g := gateService(account.NewAccount("ordinary"), user.NewUser("a@example.com", "A", "h"), 0)
	for i := 0; i < 3; i++ {
		if err := s.RequireSettledAccount(context.Background(), g.acc.ID, uuid.New()); err != nil {
			t.Fatalf("ordinary account refused: %v", err)
		}
	}
	if g.reads != 1 {
		t.Fatalf("the account was read %d times, want once", g.reads)
	}
}

// An open sign-up whose owner has not confirmed their address can do
// nothing, and is not settled.
func TestTheGateBlocksAnUnconfirmedOpenSignup(t *testing.T) {
	owner := user.NewUser("new@example.com", "New", "h")
	s, g := gateService(pendingAccount(), owner, 0)
	err := s.RequireSettledAccount(context.Background(), g.acc.ID, owner.ID)
	if !errors.Is(err, app_errors.ErrEmailVerificationRequired) {
		t.Fatalf("unconfirmed open sign-up: %v, want email verification required", err)
	}
	if g.settles != 0 {
		t.Fatal("an unconfirmed sign-up was settled")
	}
}

// Once the owner confirmed, the first request settles the account under the
// ceiling; at the ceiling it stays pending and the refusal is the ceiling's.
func TestTheGateSettlesAConfirmedOpenSignupUnderTheCeiling(t *testing.T) {
	owner := user.NewUser("new@example.com", "New", "h")
	owner.MarkEmailVerified()

	s, g := gateService(pendingAccount(), owner, 5)
	g.counts = settledCounts(5)
	if err := s.RequireSettledAccount(context.Background(), g.acc.ID, owner.ID); !isClosed(err, app_errors.ReasonAccountCeiling) {
		t.Fatalf("confirmed at the ceiling: %v, want closed for the ceiling", err)
	}
	if g.acc.OpenSignupPendingSince == nil {
		t.Fatal("the account settled past the ceiling")
	}

	g.counts = settledCounts(4)
	if err := s.RequireSettledAccount(context.Background(), g.acc.ID, owner.ID); err != nil {
		t.Fatalf("confirmed below the ceiling: %v", err)
	}
	if g.acc.OpenSignupPendingSince != nil {
		t.Fatal("the account was not settled")
	}
}

// A read the gate cannot make refuses: the gate fails closed.
func TestTheGateFailsClosed(t *testing.T) {
	s, g := gateService(pendingAccount(), user.NewUser("a@example.com", "A", "h"), 0)
	g.readErr = errors.New("database unavailable")
	if err := s.RequireSettledAccount(context.Background(), g.acc.ID, uuid.New()); err == nil {
		t.Fatal("the gate admitted a request it could not decide")
	}
}
