package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// fakeTermsRecorder stands in for the user service's acknowledgement.
type fakeTermsRecorder struct {
	calls []user_beta_notice_ack.UserBetaNoticeAckCommand
	err   error
}

func (f *fakeTermsRecorder) AcknowledgeBetaNotice(_ context.Context, cmd user_beta_notice_ack.UserBetaNoticeAckCommand) (*user.User, error) {
	f.calls = append(f.calls, cmd)
	if f.err != nil {
		return nil, f.err
	}
	u := &user.User{ID: cmd.ID}
	u.AcknowledgeBetaNotice()
	return u, nil
}

func termsService(r termsRecorder) *AuthService {
	s := &AuthService{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if r != nil {
		s.SetTermsRecorder(r)
	}
	return s
}

// A sign-up that carries the acceptance is recorded for the new user, in the
// new account, and the user returned to the client shows it.
func TestSignupWithTermsAcceptedRecordsTheAcceptance(t *testing.T) {
	rec := &fakeTermsRecorder{}
	s := termsService(rec)
	newUser := &user.User{ID: uuid.New()}
	accountID := uuid.New()

	got := s.recordSignupTerms(context.Background(), user_register.RegisterCommand{TermsAccepted: true}, newUser, accountID)

	if len(rec.calls) != 1 {
		t.Fatalf("recorder called %d times, want 1", len(rec.calls))
	}
	call := rec.calls[0]
	if call.ID != newUser.ID || call.AuthContext.UserID != newUser.ID || call.AuthContext.AccountID != accountID {
		t.Fatalf("recorded for %+v, want user %s in account %s", call, newUser.ID, accountID)
	}
	if !got.HasAcknowledgedBetaNotice() {
		t.Fatal("the returned user does not show the acceptance")
	}
}

// Without the field — an install with no terms, or an older frontend — nothing
// is recorded.
func TestSignupWithoutTermsRecordsNothing(t *testing.T) {
	rec := &fakeTermsRecorder{}
	s := termsService(rec)
	newUser := &user.User{ID: uuid.New()}

	got := s.recordSignupTerms(context.Background(), user_register.RegisterCommand{}, newUser, uuid.New())

	if len(rec.calls) != 0 {
		t.Fatalf("recorder called %d times, want 0", len(rec.calls))
	}
	if got != newUser || got.HasAcknowledgedBetaNotice() {
		t.Fatal("a sign-up without the field came back acknowledged")
	}
}

// A recording failure must not fail the registration that already happened.
func TestSignupTermsFailureKeepsTheRegistration(t *testing.T) {
	for name, s := range map[string]*AuthService{
		"recorder error": termsService(&fakeTermsRecorder{err: errors.New("audit write failed")}),
		"no recorder":    termsService(nil),
	} {
		newUser := &user.User{ID: uuid.New()}
		got := s.recordSignupTerms(context.Background(), user_register.RegisterCommand{TermsAccepted: true}, newUser, uuid.New())
		if got != newUser {
			t.Fatalf("%s: got %+v, want the registered user unchanged", name, got)
		}
		if got.HasAcknowledgedBetaNotice() {
			t.Fatalf("%s: an unrecorded acceptance is reported as stored", name)
		}
	}
}
