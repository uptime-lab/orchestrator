package auth

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	reauth_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/reauth"
)

// Why this test exists, and why it lives HERE rather than in the reauth
// package: the property is a transition across two services. A
// step-up ticket and an access token issued before a password recovery stayed
// good for the ticket's five minutes and the token's TTL after the recovery
// had revoked every session — because the reauth service bound the ticket to
// the user only and never asked the auth service whether the session was
// still alive. The test builds the real auth service, earns a real ticket
// against it, then performs the recovery and asserts the old pair is refused.

type stepUpFixture struct {
	*mandatoryFixture
	reauth  *reauth_service.Service
	session services.SessionProof
}

// newStepUpFixture logs the user in and wires a reauth service to the SAME
// auth service as its session verifier, the way main.go does.
func newStepUpFixture(t *testing.T) *stepUpFixture {
	t.Helper()
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	result := f.login(t)
	if result.Tokens == nil {
		t.Fatalf("expected an immediate login, got %+v", result)
	}
	claims, err := f.svc.ValidateToken(result.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("validating the access token: %v", err)
	}
	if claims.FamilyID == uuid.Nil || claims.IssuedAt == nil {
		t.Fatalf("the access token must name its family and mint time, got %+v", claims)
	}
	reauth := reauth_service.NewService(f.svc.userRepo, passkeyCount{n: 0}, noTwoFactor{}, f.svc.passwordHasher, slog.New(slog.DiscardHandler))
	reauth.SetSessionVerifier(f.svc)
	return &stepUpFixture{
		mandatoryFixture: f,
		reauth:           reauth,
		session:          services.SessionProof{FamilyID: claims.FamilyID, IssuedAt: claims.IssuedAt.Time},
	}
}

func (f *stepUpFixture) confirm(t *testing.T) string {
	t.Helper()
	ticket, err := f.reauth.ConfirmPassword(context.Background(), f.usr.ID, f.password, f.session)
	if err != nil {
		t.Fatalf("confirming the password on a live session: %v", err)
	}
	return ticket
}

func TestAStepUpTicketUnlocksWhileTheSessionIsLive(t *testing.T) {
	f := newStepUpFixture(t)
	ticket := f.confirm(t)
	if err := f.reauth.EnsureRecentAuth(context.Background(), f.usr.ID, ticket, f.session); err != nil {
		t.Fatalf("a fresh ticket on the live session that earned it was refused: %v", err)
	}
}

func TestAStepUpTicketDiesWithPasswordRecovery(t *testing.T) {
	f := newStepUpFixture(t)
	ticket := f.confirm(t)

	// What password recovery does: a new PasswordChangedAt, and every session
	// revoked. The ticket is still within its five minutes; the access token is
	// still within its TTL.
	changed := time.Now().Add(time.Second)
	f.usr.PasswordChangedAt = &changed
	if err := f.svc.RevokeAllSessions(context.Background(), f.usr.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	err := f.reauth.EnsureRecentAuth(context.Background(), f.usr.ID, ticket, f.session)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a pre-recovery ticket and token still unlock a credential change after recovery: %v", err)
	}
	// And the same dead session cannot simply earn a new one.
	if _, err := f.reauth.ConfirmPassword(context.Background(), f.usr.ID, f.password, f.session); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a revoked session was issued a fresh ticket: %v", err)
	}
}

func TestAStepUpTicketDiesWithSignOutEverywhere(t *testing.T) {
	// No password change this time — only the revocation. The ticket must go
	// with the family it was issued to.
	f := newStepUpFixture(t)
	ticket := f.confirm(t)
	if err := f.svc.RevokeAllSessions(context.Background(), f.usr.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	if err := f.reauth.EnsureRecentAuth(context.Background(), f.usr.ID, ticket, f.session); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a ticket outlived 'sign out everywhere': %v", err)
	}
}

func TestAStepUpTicketDiesWithItsOwnSessionsLogout(t *testing.T) {
	// Family-specific: signing out THIS device ends its ticket, while a second
	// login of the same user keeps a ticket of its own.
	f := newStepUpFixture(t)
	ticket := f.confirm(t)
	if err := f.svc.RevokeSessionFamily(context.Background(), f.usr.ID, f.session.FamilyID); err != nil {
		t.Fatalf("RevokeSessionFamily: %v", err)
	}
	if err := f.reauth.EnsureRecentAuth(context.Background(), f.usr.ID, ticket, f.session); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a ticket outlived its own session's logout: %v", err)
	}
}

func TestAStepUpTicketIsNotSpentByTheUsersOtherSession(t *testing.T) {
	f := newStepUpFixture(t)
	ticket := f.confirm(t)

	// A second, equally live login of the same user (an attacker with the
	// password, say) presents the owner's ticket.
	other := f.login(t)
	claims, err := f.svc.ValidateToken(other.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("validating the second access token: %v", err)
	}
	otherSession := services.SessionProof{FamilyID: claims.FamilyID, IssuedAt: claims.IssuedAt.Time}
	if otherSession.FamilyID == f.session.FamilyID {
		t.Fatal("the second login must be its own family")
	}
	err = f.reauth.EnsureRecentAuth(context.Background(), f.usr.ID, ticket, otherSession)
	if !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("another session of the same user spent the owner's ticket: %v", err)
	}
}
