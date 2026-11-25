package email_verification

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/email_verification"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Stubs embed their interface so an unexpected call panics instead of silently
// returning a zero value.

type sentMail struct {
	to      string
	subject string
	body    string
}

type stubSender struct {
	sent []sentMail
	err  error
}

func (s *stubSender) Send(_ context.Context, to, subject, body string) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, sentMail{to: to, subject: subject, body: body})
	return nil
}

type stubVerificationRepo struct {
	repository.EmailVerificationRepository
	created     []email_verification.EmailVerification
	claim       *email_verification.EmailVerification
	claimErr    error
	recentCount int64
	revoked     []uuid.UUID
}

func (r *stubVerificationRepo) Create(_ context.Context, v *email_verification.EmailVerification) error {
	r.created = append(r.created, *v)
	return nil
}

func (r *stubVerificationRepo) ClaimByHash(_ context.Context, _ string) (*email_verification.EmailVerification, error) {
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claim, nil
}

func (r *stubVerificationRepo) RevokeUnusedByUserID(_ context.Context, userID uuid.UUID) error {
	r.revoked = append(r.revoked, userID)
	return nil
}

func (r *stubVerificationRepo) CountRecentByUserID(_ context.Context, _ uuid.UUID, _ time.Time) (int64, error) {
	return r.recentCount, nil
}

type stubUserRepo struct {
	repository.UserRepository
	user    *user.User
	updated []*user.User
}

func (r *stubUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	if r.user == nil {
		return nil, app_errors.ErrUserNotFound
	}
	return r.user, nil
}

func (r *stubUserRepo) Update(_ context.Context, u *user.User) error {
	r.updated = append(r.updated, u)
	return nil
}

type fixture struct {
	svc    *Service
	repo   *stubVerificationRepo
	users  *stubUserRepo
	sender *stubSender
	usr    *user.User
}

func newFixture(sender Sender) *fixture {
	usr := user.NewUser("owner@example.com", "Owner", "hash")
	repo := &stubVerificationRepo{}
	users := &stubUserRepo{user: usr}
	stub, _ := sender.(*stubSender)
	return &fixture{
		svc:    NewService(repo, users, sender, "https://app.example.com/", slog.Default()),
		repo:   repo,
		users:  users,
		sender: stub,
		usr:    usr,
	}
}

func TestSendForMailsAnAbsoluteSingleUseLink(t *testing.T) {
	f := newFixture(&stubSender{})

	if err := f.svc.SendFor(context.Background(), f.usr); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(f.sender.sent) != 1 {
		t.Fatalf("expected one email, got %d", len(f.sender.sent))
	}
	mail := f.sender.sent[0]
	if mail.to != f.usr.Email {
		t.Errorf("sent to %q, want %q", mail.to, f.usr.Email)
	}
	// The base URL's trailing slash must not produce a double slash.
	if !strings.Contains(mail.body, "https://app.example.com/verify-email?token=evt_") {
		t.Errorf("expected an absolute verification link in the body, got:\n%s", mail.body)
	}
	if len(f.repo.created) != 1 {
		t.Fatalf("expected one stored verification, got %d", len(f.repo.created))
	}
	// Only the digest is persisted — a database read must not yield a usable link.
	if strings.Contains(mail.body, f.repo.created[0].TokenHash) {
		t.Error("the stored value must be a digest, not the token that was mailed")
	}
}

// A resend must invalidate the previous link, so an old email cannot be used
// after the user asks for a new one.
func TestSendForRevokesOutstandingLinks(t *testing.T) {
	f := newFixture(&stubSender{})

	if err := f.svc.SendFor(context.Background(), f.usr); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(f.repo.revoked) != 1 || f.repo.revoked[0] != f.usr.ID {
		t.Errorf("expected the user's outstanding links to be revoked, got %v", f.repo.revoked)
	}
}

func TestSendForSkipsAnAlreadyVerifiedUser(t *testing.T) {
	f := newFixture(&stubSender{})
	f.usr.MarkEmailVerified()

	err := f.svc.SendFor(context.Background(), f.usr)

	if !errors.Is(err, app_errors.ErrEmailAlreadyVerified) {
		t.Fatalf("expected ErrEmailAlreadyVerified, got %v", err)
	}
	if len(f.sender.sent) != 0 {
		t.Error("no mail must be sent to an already-verified address")
	}
}

// Named for what it actually drives: the stub is told what the count is, so it
// proves the refusal, not that repeated sends ever reach the cap. That claim
// depends on what the revoke does to the rows the count reads, which only real
// SQL can answer — see TestRepeatedVerificationSendsReachTheThrottle.
func TestSendForRefusesWhenTheThrottleBinds(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.recentCount = maxSendsPerWindow

	err := f.svc.SendFor(context.Background(), f.usr)

	if !errors.Is(err, app_errors.ErrEmailSendThrottled) {
		t.Fatalf("expected ErrEmailSendThrottled, got %v", err)
	}
	if len(f.sender.sent) != 0 {
		t.Error("a throttled request must not send mail")
	}
}

// With no transport configured the service is inert rather than broken: signup
// and email changes still work, users just stay unverified.
func TestSendForIsInertWithoutASender(t *testing.T) {
	f := newFixture(nil)

	err := f.svc.SendFor(context.Background(), f.usr)

	if !errors.Is(err, app_errors.ErrEmailNotConfigured) {
		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
	}
	if len(f.repo.created) != 0 {
		t.Error("nothing must be minted when delivery is impossible")
	}
}

func TestConfirmStampsVerification(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &email_verification.EmailVerification{
		UserID: f.usr.ID,
		Email:  f.usr.Email,
	}

	if err := f.svc.Confirm(context.Background(), "evt_whatever"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if len(f.users.updated) != 1 || f.users.updated[0].EmailVerifiedAt == nil {
		t.Fatal("expected email_verified_at to be stamped")
	}
}

// A link minted for an address the user has since changed must not verify the
// new one — otherwise changing the email would inherit the old proof.
func TestConfirmRefusesALinkForAStaleAddress(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &email_verification.EmailVerification{
		UserID: f.usr.ID,
		Email:  "previous@example.com",
	}

	err := f.svc.Confirm(context.Background(), "evt_whatever")

	if !errors.Is(err, app_errors.ErrEmailVerificationStale) {
		t.Fatalf("expected ErrEmailVerificationStale, got %v", err)
	}
	if len(f.users.updated) != 0 {
		t.Error("a stale link must not verify the current address")
	}
}

func TestConfirmRejectsAnInvalidToken(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claimErr = app_errors.ErrEmailVerificationInvalid

	err := f.svc.Confirm(context.Background(), "evt_forged")

	if !errors.Is(err, app_errors.ErrEmailVerificationInvalid) {
		t.Fatalf("expected ErrEmailVerificationInvalid, got %v", err)
	}
}

// Clicking the link twice must not look like a failure to the user.
func TestConfirmIsIdempotentForAnAlreadyVerifiedUser(t *testing.T) {
	f := newFixture(&stubSender{})
	f.usr.MarkEmailVerified()
	f.repo.claim = &email_verification.EmailVerification{
		UserID: f.usr.ID,
		Email:  f.usr.Email,
	}

	if err := f.svc.Confirm(context.Background(), "evt_whatever"); err != nil {
		t.Fatalf("expected a second confirm to succeed quietly, got %v", err)
	}
	if len(f.users.updated) != 0 {
		t.Error("an already-verified user must not be written again")
	}
}

func TestHashIsStableAndTrimsWhitespace(t *testing.T) {
	if email_verification.Hash("evt_abc") != email_verification.Hash("  evt_abc \n") {
		t.Error("surrounding whitespace from a copied link must not change the digest")
	}
	if email_verification.Hash("evt_abc") == email_verification.Hash("evt_abd") {
		t.Error("different tokens must hash differently")
	}
}

// MarkEmailVerified is the conditional column write Confirm uses;
// recorded like Update was, and refused for a moved address.
func (r *stubUserRepo) MarkEmailVerified(_ context.Context, _ uuid.UUID, email string, at time.Time) (bool, error) {
	if r.user == nil || r.user.EmailVerifiedAt != nil || user.NormalizeEmail(r.user.Email) != user.NormalizeEmail(email) {
		return false, nil
	}
	r.user.EmailVerifiedAt = &at
	r.updated = append(r.updated, r.user)
	return true, nil
}
