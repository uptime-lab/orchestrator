package two_factor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/google/uuid"
)

// enabledFactor enrols and confirms a factor, returning everything the replay
// tests need. Confirm records the enrolment code's step, so these tests also
// exercise that the ledger starts non-empty.
func enabledFactor(t *testing.T) (*Service, *stubFactorRepo, uuid.UUID, string) {
	t.Helper()
	svc, repo, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	// Confirm with the previous step's code — inside the accept window, so it
	// is honoured, but one step behind, so "now" is still spendable.
	past := time.Now().Add(-totpPeriod * time.Second)
	if _, err := svc.Confirm(context.Background(), userID, codeAt(t, enrollment.Secret, past), liveProof()); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	return svc, repo, userID, enrollment.Secret
}

// codeAt generates the code for a given moment, so a test can drive the whole
// accept window rather than only "now".
func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period: totpPeriod, Skew: 0, Digits: 6,
	})
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

// A correct code works once and is refused for the rest of its window. Before
// this, a code read over a shoulder stayed usable for up to 90 seconds — and the
// step-up path turned one such code into a five-minute confirmation ticket.
func TestVerifyRefusesAReplayedCode(t *testing.T) {
	svc, repo, userID, secret := enabledFactor(t)
	code := codeAt(t, secret, time.Now())

	if err := svc.Verify(context.Background(), userID, code); err != nil {
		t.Fatalf("first Verify = %v, want nil", err)
	}
	err := svc.Verify(context.Background(), userID, code)
	if !errors.Is(err, app_errors.ErrTwoFactorCodeAlreadyUsed) {
		t.Fatalf("second Verify = %v, want ErrTwoFactorCodeAlreadyUsed", err)
	}
	if repo.lastStep == nil {
		t.Error("the accepted step was not recorded")
	}
}

// A replay must not spend the lockout budget: the code was the user's own and
// correct, so a double-submitted form would otherwise lock them out.
func TestReplayDoesNotCountAsAFailedAttempt(t *testing.T) {
	svc, _, userID, secret := enabledFactor(t)
	code := codeAt(t, secret, time.Now())

	if err := svc.Verify(context.Background(), userID, code); err != nil {
		t.Fatalf("first Verify = %v, want nil", err)
	}
	for i := 0; i < 10; i++ {
		if err := svc.Verify(context.Background(), userID, code); !errors.Is(err, app_errors.ErrTwoFactorCodeAlreadyUsed) {
			t.Fatalf("replay %d = %v, want ErrTwoFactorCodeAlreadyUsed", i, err)
		}
	}
	// Still not locked out: the next genuine code must work.
	next := codeAt(t, secret, time.Now().Add(totpPeriod*time.Second))
	if err := svc.Verify(context.Background(), userID, next); err != nil {
		t.Fatalf("Verify with the next code = %v, want nil — replays must not lock the user out", err)
	}
}

// The whole accept window still works: a wrong skew implementation refuses valid
// codes and locks people out of a live account, which is worse than the replay.
func TestMatchTOTPStepCoversTheSkewWindow(t *testing.T) {
	svc, _, _, secret := enabledFactor(t)
	now := time.Now()

	for _, offset := range []int{0, 1, -1} {
		at := now.Add(time.Duration(offset) * totpPeriod * time.Second)
		step, ok := svc.matchTOTPStep(codeAt(t, secret, at), secret)
		if !ok {
			t.Errorf("offset %+d: code was refused", offset)
			continue
		}
		if want := at.Unix() / totpPeriod; step != want {
			t.Errorf("offset %+d: step = %d, want %d — the RECORDED step must be the one that matched", offset, step, want)
		}
	}

	// One step outside the window is not accepted at all.
	if _, ok := svc.matchTOTPStep(codeAt(t, secret, now.Add(3*totpPeriod*time.Second)), secret); ok {
		t.Error("a code from outside the skew window was accepted")
	}
}
