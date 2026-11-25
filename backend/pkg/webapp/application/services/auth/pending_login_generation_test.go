package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// Why these tests exist. A sign-in email change advances
// users.credential_generation without touching PasswordChangedAt, and the
// pending-login tickets — the second-factor challenge and the account
// selection token — carried no generation and were checked against the
// password timestamp only. A ticket earned before the change could therefore
// finish a login after it, minting a session for an identity whose sign-in
// identifier had just been repointed. Both tickets now carry the `cg` claim
// and both consumers apply the ONE policy helper an access token goes through.

func TestCompleteTwoFactorRefusesAChallengePredatingAnEmailChange(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	challenge := f.login(t)
	if challenge.TwoFactorToken == "" {
		t.Fatalf("expected a two-factor challenge, got %+v", challenge)
	}
	// The email change: generation moves, PasswordChangedAt does not.
	f.usr.CredentialGeneration++
	before := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash)

	_, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000")
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a challenge issued before the email change = %v, want ErrTokenInvalid", err)
	}
	if got := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash); got != before {
		t.Fatalf("refusal still minted a session: %d refresh records, want %d", got, before)
	}
}

func TestSelectAccountRefusesATokenPredatingAnEmailChange(t *testing.T) {
	f, challenge := newSelectFixture(t)
	f.usr.CredentialGeneration++
	before := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash)

	if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pre-auth token issued before the email change = %v, want ErrTokenInvalid", err)
	}
	if got := len(f.svc.refreshTokens.(*fakeRefreshStore).byHash); got != before {
		t.Fatalf("refusal still recorded a refresh token: %d records, want %d", got, before)
	}
}

// The controls: with the generation unchanged both tickets still complete.
func TestPendingLoginTicketsCompleteWhenTheGenerationIsUnchanged(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	challenge := f.login(t)
	if result, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000"); err != nil || result.Tokens == nil {
		t.Fatalf("an unchanged generation refused the challenge: %v, %+v", err, result)
	}

	sf, selection := newSelectFixture(t)
	if _, _, err := sf.svc.SelectAccount(context.Background(), selection.PreToken, sf.accounts[0].ID); err != nil {
		t.Fatalf("an unchanged generation refused the selection: %v", err)
	}
}

// Both tickets carry the generation they were minted at, the same claim the
// access and refresh tokens carry.
func TestPendingLoginTicketsCarryTheCredentialGeneration(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
	f.usr.CredentialGeneration = 7
	challenge := f.login(t)
	claims, err := f.svc.ValidateToken(challenge.TwoFactorToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.CredentialGeneration != 7 {
		t.Fatalf("two-factor ticket cg = %d, want 7", claims.CredentialGeneration)
	}

	sf, selection := newSelectFixture(t)
	sf.usr.CredentialGeneration = 9
	selection = sf.login(t)
	claims, err = sf.svc.ValidateToken(selection.PreToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.CredentialGeneration != 9 {
		t.Fatalf("pre-auth ticket cg = %d, want 9", claims.CredentialGeneration)
	}
}

// A ticket minted by the previous binary during the deploy minute carries no
// generation. It is tolerated exactly the way requireLiveSession tolerates a
// pre-claim access token: only the password-time test applies.
func TestPendingLoginTicketWithoutAGenerationClaimStillCompletes(t *testing.T) {
	f, _ := newSelectFixture(t)
	now := time.Now()
	legacy := &services.TokenClaims{
		UserID:    f.usr.ID,
		Email:     f.usr.Email,
		TokenType: services.TokenTypePreAuth,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    f.svc.jwtConfig.Issuer,
			Subject:   f.usr.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(preAuthTokenTTL)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, legacy).SignedString([]byte(f.svc.jwtConfig.SecretKey))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.preAuthTickets.issue(signed, f.usr.ID)
	if _, _, err := f.svc.SelectAccount(context.Background(), signed, f.accounts[0].ID); err != nil {
		t.Fatalf("a pre-claim ticket (no cg) was refused: %v", err)
	}
}
