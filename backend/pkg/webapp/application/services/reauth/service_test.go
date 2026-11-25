package reauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The service is exercised through fakes: the point under test is the policy
// (who must confirm, what a ticket unlocks), not the repositories.

type fakeUserRepo struct {
	user *user.User
	err  error
}

func (f *fakeUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

type fakePasskeyRepo struct{ count int64 }

func (f *fakePasskeyRepo) CountByUserID(_ context.Context, _ uuid.UUID) (int64, error) {
	return f.count, nil
}

type fakeTwoFactor struct {
	enabled   bool
	err       error
	factorErr error // returned by HasConfirmedFactor to exercise the fail-closed path
}

func (f *fakeTwoFactor) HasConfirmedFactor(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.enabled, f.factorErr
}
func (f *fakeTwoFactor) Verify(_ context.Context, _ uuid.UUID, _ string) error {
	return f.err
}

type fakeHasher struct{ err error }

func (f *fakeHasher) ComparePasswords(_, _ string) error { return f.err }

// fakeSessions stands in for the auth service's live-session proof. err is
// what RequireLiveSession answers; the default (nil) is "live".
type fakeSessions struct{ err error }

func (f *fakeSessions) RequireLiveSession(_ context.Context, _ uuid.UUID, _ services.SessionProof) error {
	return f.err
}

// liveSession is the proof a signed-in caller presents: a session family and
// the access token's mint time.
var liveSession = services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now()}

// newTestService assembles a service over the fakes. Only the behaviours each
// test needs are configured; the rest stay zero-valued (no credentials).
func newTestService(usr *user.User, passkeys int64, tf *fakeTwoFactor, hasher *fakeHasher) *Service {
	return &Service{
		userRepo:    &fakeUserRepo{user: usr},
		passkeyRepo: &fakePasskeyRepo{count: passkeys},
		twoFactor:   tf,
		hasher:      hasher,
		sessions:    &fakeSessions{},
		tickets:     newTicketStore(ticketTTL),
		failures:    newFailureCounter(maxFailures, failureWindow),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestEnsureRecentAuthLetsThroughAUserWithNothingToProveWith(t *testing.T) {
	// No password, no factor, no passkey AND no linked identity: nothing could
	// authenticate as this account, so demanding a proof would lock them out of
	// ever adding their first authenticator. Note this is NOT the OIDC-only
	// case any more — see the test below.
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})

	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession); err != nil {
		t.Fatalf("a user with nothing to prove with must not be asked to confirm, got %v", err)
	}
}

// fakeIdentities reports linked external identities.
type fakeIdentities struct{ count int }

func (f *fakeIdentities) ListIdentities(_ context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error) {
	out := make([]external_identity.ExternalIdentity, 0, f.count)
	for i := 0; i < f.count; i++ {
		out = append(out, external_identity.ExternalIdentity{UserID: userID})
	}
	return out, nil
}

// A federated user has no LOCAL credential but is not unable to prove
// themselves — they can be sent back to their provider. Treating them as
// "nothing to prove with" let a stolen OIDC-only session enrol the attacker's
// own passkey, which a password reset would not have removed.
func TestFederatedUserWithNoLocalCredentialMustStillConfirm(t *testing.T) {
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})
	svc.SetIdentityCounter(&fakeIdentities{count: 1})

	methods, err := svc.AvailableMethods(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("methods failed: %v", err)
	}
	if !methods.OIDC {
		t.Fatal("a linked identity must be reported as a confirmation method")
	}
	if methods.Password || methods.TOTP || methods.Passkey {
		t.Fatalf("no local credential was configured: %+v", methods)
	}

	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession); !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("expected a confirmation demand, got %v", err)
	}
}

// The ticket a completed IdP round trip grants must unlock the change, and only
// for the user it was granted to.
func TestStepUpTicketUnlocksTheFederatedUser(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})
	svc.SetIdentityCounter(&fakeIdentities{count: 1})

	ticket, err := svc.Grant(context.Background(), userID, liveSession)
	if err != nil {
		t.Fatalf("grant failed: %v", err)
	}
	if err := svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession); err != nil {
		t.Fatalf("a fresh IdP confirmation must unlock the change, got %v", err)
	}
	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), ticket, liveSession); !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("a ticket must not unlock another user, got %v", err)
	}
}

// An install with no IdP configured has no federated users, and must not start
// demanding a proof nobody can make.
func TestNoIdentityCounterKeepsTheOldExemption(t *testing.T) {
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})
	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession); err != nil {
		t.Fatalf("without OIDC wired the exemption must stand, got %v", err)
	}
}

func TestEnsureRecentAuthDemandsProofOnceAnyCredentialExists(t *testing.T) {
	cases := map[string]struct {
		usr      *user.User
		passkeys int64
		tf       *fakeTwoFactor
	}{
		"password": {usr: &user.User{HashedPassword: "hash"}, tf: &fakeTwoFactor{}},
		"totp":     {usr: &user.User{}, tf: &fakeTwoFactor{enabled: true}},
		"passkey":  {usr: &user.User{}, passkeys: 1, tf: &fakeTwoFactor{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(tc.usr, tc.passkeys, tc.tf, &fakeHasher{})
			err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession)
			if !errors.Is(err, app_errors.ErrReauthRequired) {
				t.Fatalf("expected a confirmation demand, got %v", err)
			}
		})
	}
}

func TestConfirmPasswordIssuesATicketThatUnlocks(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})

	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected a ticket")
	}
	if err := svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession); err != nil {
		t.Fatalf("the ticket should unlock, got %v", err)
	}
}

func TestATicketIsUsableMoreThanOnce(t *testing.T) {
	// One confirmation must cover a whole ceremony pair (begin then finish),
	// otherwise the user is prompted mid-flow and the flow cannot complete.
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})

	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession); err != nil {
			t.Fatalf("use %d should still unlock, got %v", i+1, err)
		}
	}
}

func TestATicketDoesNotUnlockAnotherUser(t *testing.T) {
	// Without the binding, a ticket minted anywhere would elevate whoever
	// presented it.
	owner := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})

	ticket, err := svc.ConfirmPassword(context.Background(), owner, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	err = svc.EnsureRecentAuth(context.Background(), uuid.New(), ticket, liveSession)
	if !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("another user's ticket must not unlock, got %v", err)
	}
}

func TestAnExpiredTicketStopsUnlocking(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})
	svc.tickets = newTicketStore(-time.Second) // already past its window

	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	err = svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession)
	if !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("an expired ticket must not unlock, got %v", err)
	}
}

func TestConfirmPasswordRejectsAWrongPassword(t *testing.T) {
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{},
		&fakeHasher{err: errors.New("mismatch")})

	_, err := svc.ConfirmPassword(context.Background(), uuid.New(), "wrong", liveSession)
	if !errors.Is(err, app_errors.ErrReauthInvalid) {
		t.Fatalf("expected an invalid-confirmation error, got %v", err)
	}
}

func TestConfirmPasswordIsUnavailableWithoutAPassword(t *testing.T) {
	// An OIDC user must not be told "wrong password" for a password they never
	// had — and must certainly not be let through.
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})

	_, err := svc.ConfirmPassword(context.Background(), uuid.New(), "anything", liveSession)
	if !errors.Is(err, app_errors.ErrReauthUnavailable) {
		t.Fatalf("expected an unavailable-method error, got %v", err)
	}
}

func TestRepeatedWrongPasswordsAreThrottled(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{},
		&fakeHasher{err: errors.New("mismatch")})

	for i := 0; i < maxFailures; i++ {
		if _, err := svc.ConfirmPassword(context.Background(), userID, "wrong", liveSession); !errors.Is(err, app_errors.ErrReauthInvalid) {
			t.Fatalf("attempt %d: expected an invalid-confirmation error, got %v", i+1, err)
		}
	}
	_, err := svc.ConfirmPassword(context.Background(), userID, "wrong", liveSession)
	if !errors.Is(err, app_errors.ErrReauthTooManyAttempts) {
		t.Fatalf("expected throttling after %d failures, got %v", maxFailures, err)
	}
}

func TestASuccessfulConfirmationClearsTheFailureCount(t *testing.T) {
	userID := uuid.New()
	hasher := &fakeHasher{err: errors.New("mismatch")}
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, hasher)

	for i := 0; i < maxFailures-1; i++ {
		_, _ = svc.ConfirmPassword(context.Background(), userID, "wrong", liveSession)
	}
	hasher.err = nil
	if _, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession); err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}

	hasher.err = errors.New("mismatch")
	// A fresh budget: without the clear, one more failure would trip the limit.
	for i := 0; i < maxFailures; i++ {
		if _, err := svc.ConfirmPassword(context.Background(), userID, "wrong", liveSession); errors.Is(err, app_errors.ErrReauthTooManyAttempts) {
			t.Fatalf("throttled at attempt %d; a success should have reset the count", i+1)
		}
	}
}

func TestConfirmTOTPSurfacesTheVerifierError(t *testing.T) {
	svc := newTestService(&user.User{}, 0,
		&fakeTwoFactor{enabled: true, err: app_errors.ErrTwoFactorInvalidCode}, &fakeHasher{})

	_, err := svc.ConfirmTOTP(context.Background(), uuid.New(), "000000", liveSession)
	if !errors.Is(err, app_errors.ErrTwoFactorInvalidCode) {
		t.Fatalf("expected the two-factor error to pass through, got %v", err)
	}
}

func TestAvailableMethodsReportsWhatTheUserHolds(t *testing.T) {
	svc := newTestService(&user.User{HashedPassword: "hash"}, 2, &fakeTwoFactor{enabled: true}, &fakeHasher{})

	methods, err := svc.AvailableMethods(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("failed to read methods: %v", err)
	}
	if !methods.Password || !methods.TOTP || !methods.Passkey || !methods.Any() {
		t.Fatalf("expected every method available, got %+v", methods)
	}
}

// A read error while resolving a user's methods must NOT wave a credential change
// through. The old code swallowed CountByUserID/ListIdentities/IsEnabled errors,
// so a passwordless user during a DB blip looked method-less and EnsureRecentAuth
// exempted them — the exact attack the step-up exists to stop.
func TestEnsureRecentAuthFailsClosedWhenAFactorReadErrors(t *testing.T) {
	boom := errors.New("db is momentarily unreachable")
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{factorErr: boom}, &fakeHasher{})
	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession); err == nil {
		t.Fatal("a two-factor read error must NOT let a credential change through")
	}
}
