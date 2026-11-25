package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Why these tests exist. Password login used to ask only
// whether the user had a confirmed TOTP factor. A user who had registered a
// passkey but no TOTP got a full session from the password alone — and the
// mandatory-2FA policy then counted that session as compliant, because the
// policy looks at what is ENROLLED, not at what opened the session. Against
// an account that requires two factors, a stolen password was enough.

// passkeyCount is a counter with a fixed answer, or a fixed failure.
type passkeyCount struct {
	n   int64
	err error
}

func (p passkeyCount) CountByUserID(context.Context, uuid.UUID) (int64, error) { return p.n, p.err }

// totpEnrolled is the gate of a user who confirmed a TOTP factor.
type totpEnrolled struct{}

func (totpEnrolled) HasConfirmedFactor(context.Context, uuid.UUID) (bool, error) { return true, nil }

func (totpEnrolled) Verify(context.Context, uuid.UUID, string) error { return nil }

// accountsByID answers GetByID from a map, so a multi-account user can span
// accounts with different policies.
type accountsByID struct {
	repository.AccountRepository
	byID map[uuid.UUID]*account.Account
	err  error
	// failFor makes only ONE account's row unreadable, the shape of a
	// transient error that hits the mandatory account while the lax one
	// still reads fine.
	failFor uuid.UUID
}

func (a *accountsByID) GetByID(_ context.Context, id uuid.UUID) (*account.Account, error) {
	if a.err != nil {
		return nil, a.err
	}
	if a.failFor != uuid.Nil && id == a.failFor {
		return nil, errors.New("pool timeout")
	}
	acc, ok := a.byID[id]
	if !ok {
		return nil, app_errors.ErrAccountNotFound
	}
	return acc, nil
}

type mandatoryFixture struct {
	svc      *AuthService
	usr      *user.User
	password string
	accounts []*account.Account
}

// newMandatoryFixture builds a password user who is an active member of every
// account given. requireTwoFactor sets the policy of each account in order.
func newMandatoryFixture(t *testing.T, passkeys passkeyCounter, gate twoFactorGate, requireTwoFactor ...bool) *mandatoryFixture {
	t.Helper()
	hasher := services.NewBCryptPasswordHasher(4)
	const password = "correct horse battery staple"
	hash, err := hasher.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing test password: %v", err)
	}
	usr := user.NewUser("member@example.com", "Member", hash)

	byID := map[uuid.UUID]*account.Account{}
	var memberships []account_member.AccountMember
	var accounts []*account.Account
	for i, required := range requireTwoFactor {
		acc := account.NewAccount("Account " + string(rune('A'+i)))
		acc.RequireTwoFactor = required
		byID[acc.ID] = acc
		accounts = append(accounts, acc)
		memberships = append(memberships, *account_member.NewAccountMember(acc.ID, usr.ID))
	}

	svc := &AuthService{
		jwtConfig:        config.JWTConfig{SecretKey: "test-secret", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour, Issuer: "test"},
		userRepo:         &stubUserRepo{byEmail: usr, byID: usr},
		accountRepo:      &accountsByID{byID: byID},
		memberRepo:       &stubMemberRepo{memberships: memberships},
		refreshTokens:    newFakeRefreshStore(),
		twoFactor:        gate,
		passkeys:         passkeys,
		twoFactorTickets: newTicketStore(preAuthTokenTTL),
		preAuthTickets:   newTicketStore(preAuthTokenTTL),
		passwordHasher:   hasher,
		logger:           slog.Default(),
	}
	return &mandatoryFixture{svc: svc, usr: usr, password: password, accounts: accounts}
}

func (f *mandatoryFixture) login(t *testing.T) *services.LoginResult {
	t.Helper()
	result, err := f.svc.Login(f.usr.Email, f.password, "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return result
}

// assertChallenged is the property the fix must hold: a second factor is
// owed, and NOTHING else was issued — no tokens, no pre-auth ticket, no
// account list a stolen password could enumerate.
func assertChallenged(t *testing.T, result *services.LoginResult, wantMethods ...string) {
	t.Helper()
	if result.TwoFactorToken == "" {
		t.Fatal("password alone produced a login: no second-factor challenge was issued")
	}
	if result.Tokens != nil || result.PreToken != "" || len(result.Accounts) != 0 {
		t.Fatalf("a second-factor challenge must issue nothing else, got %+v", result)
	}
	if len(result.TwoFactorMethods) != len(wantMethods) {
		t.Fatalf("methods = %v, want %v", result.TwoFactorMethods, wantMethods)
	}
	for i := range wantMethods {
		if result.TwoFactorMethods[i] != wantMethods[i] {
			t.Fatalf("methods = %v, want %v", result.TwoFactorMethods, wantMethods)
		}
	}
}

func TestPasswordAloneDoesNotSatisfyMandatoryTwoFactorForAPasskeyHolder(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, true)
	assertChallenged(t, f.login(t), services.TwoFactorMethodPasskey)
}

func TestPasskeyHolderInAMandatoryAccountIsChallengedOnTheAccountSelectionPathToo(t *testing.T) {
	// Two accounts, only the second mandates 2FA: the challenge must still
	// come BEFORE account selection, or the user would pick the lax account
	// with a password and switch into the strict one from a live session.
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, false, true)
	assertChallenged(t, f.login(t), services.TwoFactorMethodPasskey)
}

func TestPasskeyHolderOutsideMandatoryAccountsKeepsThePasswordLogin(t *testing.T) {
	// Unchanged behaviour: the passkey is an alternative way in, not a factor
	// bolted onto the password, and demanding it would lock out a user who
	// lost the authenticator — recovery codes exist only for TOTP.
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, false)
	result := f.login(t)
	if result.TwoFactorToken != "" {
		t.Fatal("a passkey holder outside mandatory-2FA accounts must not be challenged")
	}
	if result.Tokens == nil {
		t.Fatal("expected a session for a single-account user")
	}
}

func TestPasswordOnlyUserInAMandatoryAccountStillLogsIn(t *testing.T) {
	// No factor at all: the login succeeds and the enrollment middleware then
	// blocks the API until one is set up. A challenge nobody could complete
	// would be a lockout, not a policy.
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, true)
	result := f.login(t)
	if result.TwoFactorToken != "" || result.Tokens == nil {
		t.Fatalf("expected a plain session for a user with nothing enrolled, got %+v", result)
	}
}

func TestTOTPChallengeAdvertisesThePasskeyToo(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 2}, totpEnrolled{}, false)
	assertChallenged(t, f.login(t), services.TwoFactorMethodTOTP, services.TwoFactorMethodPasskey)
}

func TestTOTPChallengeAloneWhenNoPasskeyExists(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	assertChallenged(t, f.login(t), services.TwoFactorMethodTOTP)
}

func TestLoginFailsClosedWhenPasskeysCannotBeCounted(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{err: errors.New("database unavailable")}, noTwoFactor{}, true)
	if _, err := f.svc.Login(f.usr.Email, f.password, ""); err == nil {
		t.Fatal("a passkey count failure must refuse the login, not skip the second factor")
	}
}

func TestLoginFailsClosedWhenThePolicyCannotBeRead(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, true)
	f.svc.memberRepo = failingMemberRepo{}
	if _, err := f.svc.Login(f.usr.Email, f.password, ""); err == nil {
		t.Fatal("a membership read failure must refuse the login, not skip the second factor")
	}
}

// failingMemberRepo cannot list memberships — the read the policy rests on.
type failingMemberRepo struct {
	repository.AccountMemberRepository
}

func (failingMemberRepo) ListByUserID(context.Context, uuid.UUID) ([]account_member.AccountMember, error) {
	return nil, errors.New("database unavailable")
}

// notEnrolledGate is the real service's answer to a code from a user who
// never confirmed TOTP: two_factor.Service.Verify returns
// ErrTwoFactorNotEnrolled for a missing or unconfirmed factor.
type notEnrolledGate struct{}

func (notEnrolledGate) HasConfirmedFactor(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

func (notEnrolledGate) Verify(context.Context, uuid.UUID, string) error {
	return app_errors.ErrTwoFactorNotEnrolled
}

func TestAPasskeyOnlyChallengeCannotBeCompletedWithACode(t *testing.T) {
	// The ticket issued to a passkey-holder must not open a TOTP side door:
	// CompleteTwoFactor spends it and the gate refuses a user without TOTP.
	f := newMandatoryFixture(t, passkeyCount{n: 1}, notEnrolledGate{}, true)
	result := f.login(t)
	assertChallenged(t, result, services.TwoFactorMethodPasskey)
	if _, err := f.svc.CompleteTwoFactor(context.Background(), result.TwoFactorToken, "123456"); !errors.Is(err, app_errors.ErrTwoFactorNotEnrolled) {
		t.Fatalf("CompleteTwoFactor = %v, want ErrTwoFactorNotEnrolled", err)
	}
	// And the ticket was spent by that attempt.
	if _, err := f.svc.CompleteTwoFactor(context.Background(), result.TwoFactorToken, "123456"); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a second attempt on the same ticket = %v, want ErrTokenInvalid", err)
	}
}

// A transient failure reading the mandatory account's row must REFUSE the
// login, never read as "no mandatory account": ListAccounts skips any
// unreadable account (right for a picker, wrong here), which is why the check
// no longer rests on it.
func TestLoginFailsClosedWhenAnAccountRowCannotBeRead(t *testing.T) {
	// Mandatory account first, lax account second; only the mandatory row
	// fails to read. Skipping it would leave a single readable lax account
	// and a session issued for it from the password alone.
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, true, false)
	f.svc.accountRepo.(*accountsByID).failFor = f.accounts[0].ID
	result, err := f.svc.Login(f.usr.Email, f.password, "")
	if err == nil {
		t.Fatalf("login succeeded past an unreadable account row: %+v", result)
	}
	if result != nil {
		t.Fatalf("a refused login must issue nothing, got %+v", result)
	}
}

// A genuinely MISSING account (a stale membership row) is skipped, as before.
func TestLoginSkipsAMembershipWhoseAccountIsGone(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, false)
	stale := *account_member.NewAccountMember(uuid.New(), f.usr.ID)
	repo := f.svc.memberRepo.(*stubMemberRepo)
	repo.memberships = append(repo.memberships, stale)
	if _, err := f.svc.Login(f.usr.Email, f.password, ""); err != nil {
		t.Fatalf("a stale membership refused the login: %v", err)
	}
}

