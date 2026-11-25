package auth

import (
	"context"
	"errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// Why these tests exist. A
// login in progress — password accepted, second factor or account selection
// still owed — is a credential earned with the password. It used to outlive
// two things that are meant to end every credential: a password change
// (CompleteTwoFactor never compared the challenge's issue time with
// PasswordChangedAt) and "sign out everywhere" (RevokeAllSessions revoked
// refresh tokens only, and the in-memory ticket stores were never told).

func TestCompleteTwoFactorRefusesAChallengePredatingThePasswordChange(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	challenge := f.login(t)
	if challenge.TwoFactorToken == "" {
		t.Fatalf("expected a two-factor challenge, got %+v", challenge)
	}
	changed := time.Now().Add(time.Second)
	f.usr.PasswordChangedAt = &changed
	before := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash)

	_, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000")
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a challenge issued before the password change = %v, want ErrTokenInvalid", err)
	}
	if got := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash); got != before {
		t.Fatalf("refusal still minted a session: %d refresh records, want %d", got, before)
	}
}

func TestCompleteTwoFactorAllowsAChallengeIssuedAfterThePasswordChange(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	changed := time.Now().Add(-time.Minute)
	f.usr.PasswordChangedAt = &changed
	challenge := f.login(t)
	result, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000")
	if err != nil {
		t.Fatalf("a challenge issued after the password change was refused: %v", err)
	}
	if result.Tokens == nil {
		t.Fatalf("expected a session, got %+v", result)
	}
}

func TestRevokeAllSessionsEndsAPendingTwoFactorChallenge(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	challenge := f.login(t)
	if err := f.svc.RevokeAllSessions(context.Background(), f.usr.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	if _, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000"); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pending second-factor challenge survived 'sign out everywhere': %v", err)
	}
}

func TestRevokeAllSessionsEndsAPendingAccountSelection(t *testing.T) {
	f, challenge := newSelectFixture(t)
	if err := f.svc.RevokeAllSessions(context.Background(), f.usr.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pending account selection survived 'sign out everywhere': %v", err)
	}
}

func TestRevokeOtherSessionsEndsPendingLogins(t *testing.T) {
	// A login in progress is never the caller's own session, so "sign out
	// everywhere else" ends it too.
	f, challenge := newSelectFixture(t)
	if err := f.svc.RevokeOtherSessions(context.Background(), f.usr.ID, uuid.New()); err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}
	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pending account selection survived 'sign out everywhere else': %v", err)
	}
}

func TestRevokingOneUsersPendingLoginsLeavesAnothersStanding(t *testing.T) {
	store := newTicketStore(time.Minute)
	alice, bob := uuid.New(), uuid.New()
	store.issue("alice-ticket", alice)
	store.issue("bob-ticket", bob)
	store.revokeUser(alice)
	if store.consume("alice-ticket") {
		t.Fatal("alice's pending ticket survived her revocation")
	}
	if !store.consume("bob-ticket") {
		t.Fatal("bob's pending ticket was revoked with alice's")
	}
}

// Two refresh tokens minted in the same second for the same family must be
// different tokens: `iat` is whole seconds, and without a per-token id the
// successor of a rotation was byte-identical to its predecessor, so its stored
// hash collided with the row it succeeded (a 500 on /auth/refresh within a
// second of login).
func TestTokensMintedInTheSameSecondAreDistinct(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	family := uuid.New()
	// Retry until both mints land in the same wall-clock second, so the
	// test proves the property rather than the clock.
	var first, second *services.TokenPair
	var rec1, rec2 *refresh_token.RefreshToken
	for attempt := 0; attempt < 5; attempt++ {
		var err error
		first, rec1, err = f.svc.mintTokenPair(f.usr, f.accounts[0].ID, family)
		if err != nil {
			t.Fatal(err)
		}
		second, rec2, err = f.svc.mintTokenPair(f.usr, f.accounts[0].ID, family)
		if err != nil {
			t.Fatal(err)
		}
		c1, _ := f.svc.ValidateToken(first.RefreshToken)
		c2, _ := f.svc.ValidateToken(second.RefreshToken)
		if c1.IssuedAt.Time.Equal(c2.IssuedAt.Time) {
			break
		}
		if attempt == 4 {
			t.Fatal("could not mint two tokens in one second")
		}
	}
	if first.RefreshToken == second.RefreshToken || rec1.TokenHash == rec2.TokenHash {
		t.Fatal("two refresh tokens minted back to back in one family are identical")
	}
	if first.AccessToken == second.AccessToken {
		t.Fatal("two access tokens minted back to back are identical")
	}
}

func TestRefreshImmediatelyAfterLoginRotates(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	// The refresh path re-checks the membership by account; the list stub
	// does not answer that, so use the always-active one.
	f.svc.memberRepo = twoAccountMembers{stubMemberRepo: f.svc.memberRepo.(*stubMemberRepo)}
	result := f.login(t)
	pair, err := f.svc.RefreshToken(result.Tokens.RefreshToken)
	if err != nil {
		t.Fatalf("refresh within the login's second: %v", err)
	}
	if pair.RefreshToken == result.Tokens.RefreshToken {
		t.Fatal("rotation returned the presented token")
	}
	if _, err := f.svc.RefreshToken(pair.RefreshToken); err != nil {
		t.Fatalf("second rotation within the same second: %v", err)
	}
}
