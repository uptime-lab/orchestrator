package reauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Why these tests exist. A step-up ticket used to be bound to
// the USER only, and neither its issue nor its use asked whether the caller's
// session was still alive. The access token is stateless and keeps verifying
// until its TTL after a sign-out, a "sign out everywhere" or a password reset,
// so a ticket and token captured before a recovery could change the recovered
// password afterwards — the guard in front of every credential change was the
// one thing recovery did not reach.

func TestATicketIsBoundToTheSessionThatEarnedIt(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})

	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	otherSession := services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now()}
	err = svc.EnsureRecentAuth(context.Background(), userID, ticket, otherSession)
	if !errors.Is(err, app_errors.ErrReauthRequired) {
		t.Fatalf("the user's OTHER session spent a ticket it never earned: %v", err)
	}
	if err := svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession); err != nil {
		t.Fatalf("the session that earned the ticket must still be able to use it: %v", err)
	}
}

func TestAnUnknownSessionFamilyNeverMatchesATicket(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})
	// A token minted before family ids existed carries none. The verifier
	// refuses it anyway, but the store must not treat Nil as a wildcard either.
	nilFamily := services.SessionProof{IssuedAt: time.Now()}
	ticket := svc.tickets.put(userID, uuid.Nil)
	if svc.tickets.valid(ticket, userID, nilFamily.FamilyID) {
		t.Fatal("a ticket with no family matched a proof with no family")
	}
}

func TestADeadSessionCannotSpendAnEarlierTicket(t *testing.T) {
	// The ticket was earned while the session was live; then the user signed
	// out everywhere (or reset the password). The verifier now says the
	// session is gone, and that answer must win over the still-unexpired
	// ticket — and it must NOT read as "please confirm again", because the
	// same dead session could not obtain a confirmation either.
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})
	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}

	svc.sessions = &fakeSessions{err: app_errors.ErrTokenInvalid}
	err = svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a dead session spent a ticket earned while it was alive: %v", err)
	}
}

func TestADeadSessionIsNotIssuedATicket(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{enabled: true}, &fakeHasher{})
	svc.sessions = &fakeSessions{err: app_errors.ErrTokenInvalid}

	if _, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("password confirmation on a dead session = %v, want ErrTokenInvalid", err)
	}
	if _, err := svc.ConfirmTOTP(context.Background(), userID, "000000", liveSession); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("TOTP confirmation on a dead session = %v, want ErrTokenInvalid", err)
	}
	if _, err := svc.Grant(context.Background(), userID, liveSession); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a granted proof on a dead session = %v, want ErrTokenInvalid", err)
	}
}

func TestTheCredentiallessExemptionAlsoNeedsALiveSession(t *testing.T) {
	// The "nothing to prove with" exemption lets a user add their first
	// authenticator. It must not let a session that no longer exists do it.
	svc := newTestService(&user.User{}, 0, &fakeTwoFactor{}, &fakeHasher{})
	svc.sessions = &fakeSessions{err: app_errors.ErrTokenInvalid}
	if err := svc.EnsureRecentAuth(context.Background(), uuid.New(), "", liveSession); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("the exemption waved a dead session through: %v", err)
	}
}

func TestNoSessionVerifierFailsClosed(t *testing.T) {
	// A wiring omission must refuse every step-up, loudly, never silently
	// restore the user-only binding.
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})
	svc.sessions = nil
	if _, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession); err == nil {
		t.Fatal("a ticket was issued with no session verifier wired")
	}
	if err := svc.EnsureRecentAuth(context.Background(), userID, "", liveSession); err == nil {
		t.Fatal("a credential change was let through with no session verifier wired")
	}
}

func TestAnUnreadableSessionFailsClosed(t *testing.T) {
	userID := uuid.New()
	svc := newTestService(&user.User{HashedPassword: "hash"}, 0, &fakeTwoFactor{}, &fakeHasher{})
	ticket, err := svc.ConfirmPassword(context.Background(), userID, "correct", liveSession)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	boom := errors.New("pool timeout")
	svc.sessions = &fakeSessions{err: boom}
	if err := svc.EnsureRecentAuth(context.Background(), userID, ticket, liveSession); !errors.Is(err, boom) {
		t.Fatalf("a session read failure must refuse with its own error, got %v", err)
	}
}
