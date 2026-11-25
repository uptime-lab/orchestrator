package password_reset

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
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/password_reset"
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

type stubResetRepo struct {
	repository.PasswordResetRepository
	created     []password_reset.PasswordReset
	claim       *password_reset.PasswordReset
	claimErr    error
	recentCount int64
	revoked     []uuid.UUID
	// claims counts ClaimByHash calls: each one spends the link.
	claims int
}

func (r *stubResetRepo) Create(_ context.Context, reset *password_reset.PasswordReset) error {
	r.created = append(r.created, *reset)
	return nil
}

func (r *stubResetRepo) ClaimByHash(_ context.Context, _ string) (*password_reset.PasswordReset, error) {
	r.claims++
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claim, nil
}

func (r *stubResetRepo) RevokeUnusedByUserID(_ context.Context, userID uuid.UUID) error {
	r.revoked = append(r.revoked, userID)
	return nil
}

func (r *stubResetRepo) CountRecentByUserID(_ context.Context, _ uuid.UUID, _ time.Time) (int64, error) {
	return r.recentCount, nil
}

func (r *stubResetRepo) DeleteExpired(_ context.Context) error { return nil }

type stubUserRepo struct {
	repository.UserRepository
	user    *user.User
	getErr  error
	updated []*user.User
	// emailAfterRead, when set, is the address the row moves to the moment
	// the service has read its snapshot — the concurrent email change that
	// commits between the address check and the password write. The
	// snapshot handed back still shows the old one.
	emailAfterRead string
}

func (r *stubUserRepo) GetByEmail(_ context.Context, _ string) (*user.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.user, nil
}

func (r *stubUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	if r.user == nil {
		return nil, app_errors.ErrUserNotFound
	}
	if r.emailAfterRead != "" {
		snapshot := *r.user
		r.user.Email = r.emailAfterRead
		r.emailAfterRead = ""
		return &snapshot, nil
	}
	return r.user, nil
}

// SetPasswordIfEmail applies the SQL statement's WHERE against the STORED
// address, which is what makes the concurrent change above visible.
func (r *stubUserRepo) SetPasswordIfEmail(ctx context.Context, id uuid.UUID, hashed, expectedEmail string, at time.Time) error {
	if r.user == nil {
		return app_errors.ErrUserNotFound
	}
	if user.NormalizeEmail(r.user.Email) != user.NormalizeEmail(expectedEmail) {
		return app_errors.ErrPasswordResetStale
	}
	return r.SetPassword(ctx, id, hashed, at)
}

func (r *stubUserRepo) Update(_ context.Context, u *user.User) error {
	r.updated = append(r.updated, u)
	return nil
}

type fixture struct {
	svc    *Service
	repo   *stubResetRepo
	users  *stubUserRepo
	sender *stubSender
	usr    *user.User
}

func newFixture(sender Sender) *fixture {
	usr := user.NewUser("owner@example.com", "Owner", "existing-hash")
	repo := &stubResetRepo{}
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

func TestRequestMailsAnAbsoluteSingleUseLink(t *testing.T) {
	f := newFixture(&stubSender{})

	if err := f.svc.Request(context.Background(), "Owner@Example.com "); err != nil {
		t.Fatalf("request: %v", err)
	}

	if len(f.sender.sent) != 1 {
		t.Fatalf("expected one email, got %d", len(f.sender.sent))
	}
	mail := f.sender.sent[0]
	if mail.to != f.usr.Email {
		t.Errorf("sent to %q, want %q", mail.to, f.usr.Email)
	}
	// The base URL's trailing slash must not produce a double slash.
	if !strings.Contains(mail.body, "https://app.example.com/reset-password?token=prt_") {
		t.Errorf("expected an absolute reset link in the body, got:\n%s", mail.body)
	}
	if len(f.repo.created) != 1 {
		t.Fatalf("expected one stored grant, got %d", len(f.repo.created))
	}
	// Only the digest is persisted — a database read must not yield a usable link.
	if strings.Contains(mail.body, f.repo.created[0].TokenHash) {
		t.Error("the stored value must be a digest, not the token that was mailed")
	}
}

// The reset link is a takeover credential; a second request must kill the first
// one rather than leave two live links in a mailbox.
func TestRequestRevokesOutstandingGrants(t *testing.T) {
	f := newFixture(&stubSender{})

	if err := f.svc.Request(context.Background(), f.usr.Email); err != nil {
		t.Fatalf("request: %v", err)
	}

	if len(f.repo.revoked) != 1 || f.repo.revoked[0] != f.usr.ID {
		t.Errorf("expected outstanding grants to be revoked, got %v", f.repo.revoked)
	}
}

// The endpoint must not become a directory of registered users: an unknown
// address answers exactly like a successful one.
func TestRequestIsSilentForAnUnknownAddress(t *testing.T) {
	f := newFixture(&stubSender{})
	f.users.getErr = app_errors.ErrUserNotFound

	if err := f.svc.Request(context.Background(), "stranger@example.com"); err != nil {
		t.Fatalf("an unknown address must not surface an error, got %v", err)
	}
	if len(f.sender.sent) != 0 || len(f.repo.created) != 0 {
		t.Error("nothing may be minted or mailed for an unknown address")
	}
}

// An OIDC-only identity has no password to reset. Minting one here would let
// mailbox control alone graft a password onto a Google-only account.
func TestRequestSkipsAPasswordlessAccount(t *testing.T) {
	f := newFixture(&stubSender{})
	f.usr.HashedPassword = ""

	if err := f.svc.Request(context.Background(), f.usr.Email); err != nil {
		t.Fatalf("request: %v", err)
	}
	if len(f.sender.sent) != 0 || len(f.repo.created) != 0 {
		t.Error("a passwordless account must not receive a reset link")
	}
}

// Named for what it actually drives: the stub is told what the count is, so it
// proves the refusal, not that repeated requests ever reach the cap. That claim
// depends on what the revoke does to the rows the count reads, which only real
// SQL can answer — see TestRepeatedResetRequestsReachTheThrottle.
func TestRequestStaysSilentWhenTheThrottleBinds(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.recentCount = maxSendsPerWindow

	if err := f.svc.Request(context.Background(), f.usr.Email); err != nil {
		t.Fatalf("a throttled request must stay silent, got %v", err)
	}
	if len(f.sender.sent) != 0 {
		t.Error("a throttled request must not send mail")
	}
}

// Without a transport the flow reports itself unavailable, so the UI can point
// the user at an administrator instead of pretending mail is on its way.
func TestRequestReportsAnUnconfiguredDeployment(t *testing.T) {
	f := newFixture(nil)

	err := f.svc.Request(context.Background(), f.usr.Email)

	if !errors.Is(err, app_errors.ErrEmailNotConfigured) {
		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
	}
	if len(f.repo.created) != 0 {
		t.Error("nothing must be minted when delivery is impossible")
	}
}

func TestConfirmInstallsTheNewPasswordAndEndsOtherSessions(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &password_reset.PasswordReset{UserID: f.usr.ID, Email: f.usr.Email}

	if err := f.svc.Confirm(context.Background(), "prt_whatever", "a-new-password"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if len(f.users.updated) != 1 {
		t.Fatalf("expected the user to be written once, got %d writes", len(f.users.updated))
	}
	updated := f.users.updated[0]
	if updated.HashedPassword == "existing-hash" || updated.HashedPassword == "a-new-password" {
		t.Error("the new password must be stored as a fresh hash")
	}
	// AuthService.RefreshToken rejects refresh tokens issued before this stamp,
	// which is what signs out a session an attacker may be holding.
	if updated.PasswordChangedAt == nil {
		t.Error("password_changed_at must be stamped so older refresh tokens stop working")
	}
	// Receiving the link proved mailbox control — the same proof verification asks for.
	if updated.EmailVerifiedAt == nil {
		t.Error("completing a reset must also mark the address verified")
	}
}

// Losing an old mailbox must not mean losing the account: a link mailed to a
// since-changed address cannot reset the current one.
func TestConfirmRefusesALinkForAStaleAddress(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &password_reset.PasswordReset{UserID: f.usr.ID, Email: "previous@example.com"}

	err := f.svc.Confirm(context.Background(), "prt_whatever", "a-new-password")

	if !errors.Is(err, app_errors.ErrPasswordResetStale) {
		t.Fatalf("expected ErrPasswordResetStale, got %v", err)
	}
	if len(f.users.updated) != 0 {
		t.Error("a stale link must not change the password")
	}
}

// The address check above reads a snapshot; the write used to
// predicate on the id alone. An email change that commits in between — the
// owner moving the account away from a mailbox somebody else now reads —
// therefore did not stop that mailbox's grant from replacing the password.
// The write now carries the grant's address in its own WHERE.
func TestConfirmRefusesWhenTheAddressMovesBetweenCheckAndWrite(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &password_reset.PasswordReset{UserID: f.usr.ID, Email: f.usr.Email}
	f.users.emailAfterRead = "moved@example.com"

	err := f.svc.Confirm(context.Background(), "prt_whatever", "a-new-password")

	if !errors.Is(err, app_errors.ErrPasswordResetStale) {
		t.Fatalf("a grant for an address that moved before the write = %v, want ErrPasswordResetStale", err)
	}
	if len(f.users.updated) != 0 || f.usr.HashedPassword != "existing-hash" || f.usr.PasswordChangedAt != nil {
		t.Fatalf("the password was replaced through a stale mailbox: writes=%d hash=%q", len(f.users.updated), f.usr.HashedPassword)
	}
}

func TestConfirmRejectsAnInvalidToken(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claimErr = app_errors.ErrPasswordResetInvalid

	err := f.svc.Confirm(context.Background(), "prt_forged", "a-new-password")

	if !errors.Is(err, app_errors.ErrPasswordResetInvalid) {
		t.Fatalf("expected ErrPasswordResetInvalid, got %v", err)
	}
}

// The password rule is checked BEFORE the token is claimed, so a user who
// fumbles the form does not burn their single-use link.
func TestConfirmRejectsAShortPasswordWithoutSpendingTheToken(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &password_reset.PasswordReset{UserID: f.usr.ID, Email: f.usr.Email}

	err := f.svc.Confirm(context.Background(), "prt_whatever", "short")

	// TooShort, not Required: a password that is present but under the floor is
	// a different mistake from omitting one, and the two used to share an error
	// — which is how the sign-up form ended up telling people to supply the
	// password they had just typed.
	if !errors.Is(err, app_errors.ErrUserPasswordTooShort) {
		t.Fatalf("expected ErrUserPasswordTooShort, got %v", err)
	}
	if len(f.users.updated) != 0 {
		t.Error("a rejected password must not be written")
	}
}

// A password bcrypt cannot hash is refused before the claim: it used to spend
// the link first and then fail the hash, so the user's retry with a valid
// password found the link already used.
func TestConfirmRejectsAnOverlongPasswordWithoutSpendingTheToken(t *testing.T) {
	f := newFixture(&stubSender{})
	f.repo.claim = &password_reset.PasswordReset{UserID: f.usr.ID, Email: f.usr.Email}

	err := f.svc.Confirm(context.Background(), "prt_whatever", strings.Repeat("A", user.MaxPasswordLength+8))

	if !errors.Is(err, app_errors.ErrUserPasswordTooLong) {
		t.Fatalf("expected ErrUserPasswordTooLong, got %v", err)
	}
	if f.repo.claims != 0 {
		t.Fatalf("the link was claimed %d times for a password that cannot be stored", f.repo.claims)
	}
	if len(f.users.updated) != 0 {
		t.Error("a rejected password must not be written")
	}
	// The same link still works with a password that fits.
	if err := f.svc.Confirm(context.Background(), "prt_whatever", "a-new-password"); err != nil {
		t.Fatalf("the retry with a valid password must succeed, got %v", err)
	}
}

func TestHashIsStableAndTrimsWhitespace(t *testing.T) {
	if password_reset.Hash("prt_abc") != password_reset.Hash("  prt_abc \n") {
		t.Error("surrounding whitespace from a copied link must not change the digest")
	}
	if password_reset.Hash("prt_abc") == password_reset.Hash("prt_abd") {
		t.Error("different tokens must hash differently")
	}
}

// The column-specific transitions, recorded like Update was.
func (r *stubUserRepo) SetPassword(_ context.Context, _ uuid.UUID, hashed string, at time.Time) error {
	if r.user != nil {
		r.user.HashedPassword, r.user.PasswordChangedAt, r.user.CredentialGeneration = hashed, &at, r.user.CredentialGeneration+1
		r.updated = append(r.updated, r.user)
	}
	return nil
}

func (r *stubUserRepo) MarkEmailVerified(_ context.Context, _ uuid.UUID, email string, at time.Time) (bool, error) {
	if r.user == nil || r.user.EmailVerifiedAt != nil || user.NormalizeEmail(r.user.Email) != user.NormalizeEmail(email) {
		return false, nil
	}
	r.user.EmailVerifiedAt = &at
	return true, nil
}
