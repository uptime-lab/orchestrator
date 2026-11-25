package user

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_password_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

type fakeUserRepo struct {
	users map[uuid.UUID]*user.User
	// sessionDead makes the session-bound writes refuse, the way the SQL
	// boundary does once the family was revoked or the generation moved.
	sessionDead bool
	// lastProfileWrite / lastPasswordProof are what the session-bound writes
	// were handed.
	lastProfileWrite  *recordedWrite
	lastPasswordProof *sessionArgs
}

func (f *fakeUserRepo) Create(_ context.Context, u *user.User) error {
	f.users[u.ID] = u
	return nil
}

func (f *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*user.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, context.Canceled
	}
	copied := *u
	return &copied, nil
}

func (f *fakeUserRepo) GetByEmail(_ context.Context, _ string) (*user.User, error) {
	return nil, context.Canceled
}

func (f *fakeUserRepo) ListByEmailIgnoringCase(_ context.Context, _ string) ([]*user.User, error) {
	return nil, context.Canceled
}

func (f *fakeUserRepo) Update(_ context.Context, u *user.User) error {
	copied := *u
	f.users[u.ID] = &copied
	return nil
}

func (f *fakeUserRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(f.users, id)
	return nil
}

func (f *fakeUserRepo) ListByAccountID(_ context.Context, _ uuid.UUID, _, _ int) ([]*user.User, int, error) {
	return nil, 0, nil
}

// fakeAuditRepo records events in memory and can be told to fail, so the
// "consent is not stored without an audit trail" rule is testable.
type fakeAuditRepo struct {
	events []*audit_event.AuditEvent
	err    error
}

func (f *fakeAuditRepo) Create(_ context.Context, e *audit_event.AuditEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAuditRepo) CountByAccountAndActionsSince(_ context.Context, _ uuid.UUID, _ []string, _ time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeAuditRepo) List(_ context.Context, _ uuid.UUID, _ repository.AuditEventListFilter, _, _ int) ([]audit_event.AuditEvent, int64, error) {
	return nil, 0, nil
}

func newTestService(existing *user.User) (*Service, *fakeUserRepo) {
	svc, repo, _ := newTestServiceWithAudit(existing)
	return svc, repo
}

func newTestServiceWithAudit(existing *user.User) (*Service, *fakeUserRepo, *fakeAuditRepo) {
	repo := &fakeUserRepo{users: map[uuid.UUID]*user.User{existing.ID: existing}}
	audit := &fakeAuditRepo{}
	return NewUserService(repo, audit, slog.New(slog.DiscardHandler)), repo, audit
}

func verifiedUser(email string) *user.User {
	now := time.Now()
	return &user.User{
		ID:              uuid.New(),
		Name:            "Test",
		Email:           email,
		EmailVerifiedAt: &now,
	}
}

func updateCmd(u *user.User, newEmail string) user_update.UserUpdateCommand {
	name := "Test"
	return user_update.UserUpdateCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Name:        &name,
		Email:       &newEmail,
	}
}

// A REAL email change must clear the verification stamp: proven mailbox
// ownership no longer holds, and email_verified_at gates OIDC auto-linking
// to a password account.
func TestUpdateMeRealEmailChangeClearsVerification(t *testing.T) {
	u := verifiedUser("old@example.com")
	svc, _ := newTestService(u)

	updated, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession())
	if err != nil {
		t.Fatal(err)
	}
	if updated.EmailVerifiedAt != nil {
		t.Fatal("email change must clear EmailVerifiedAt")
	}
	if updated.Email != "new@example.com" {
		t.Fatalf("email not updated: %s", updated.Email)
	}
}

// A case-only change of the same address is NOT an ownership change and must
// keep the verification stamp (comparison is on normalized emails).
func TestUpdateMeCaseOnlyChangeKeepsVerification(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, _ := newTestService(u)

	updated, err := svc.UpdateMe(context.Background(), updateCmd(u, "Owner@Example.COM"), liveSession())
	if err != nil {
		t.Fatal(err)
	}
	if updated.EmailVerifiedAt == nil {
		t.Fatal("case-only change must keep EmailVerifiedAt")
	}
	if updated.Email != "owner@example.com" {
		t.Fatalf("email must be persisted normalized, got %s", updated.Email)
	}
}

// The persisted email is always normalized — raw bytes could slip a
// case-variant duplicate past the byte-wise unique index.
func TestUpdateMePersistsNormalizedEmail(t *testing.T) {
	u := verifiedUser("a@b.c")
	svc, repo := newTestService(u)

	if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "  MiXeD@CaSe.ExAmPlE.CoM  "), liveSession()); err != nil {
		t.Fatal(err)
	}
	stored := repo.users[u.ID]
	if stored.Email != "mixed@case.example.com" {
		t.Fatalf("stored email not normalized: %q", stored.Email)
	}
	if stored.EmailVerifiedAt != nil {
		t.Fatal("real change must clear verification")
	}
}

func ackCmd(u *user.User) user_beta_notice_ack.UserBetaNoticeAckCommand {
	return user_beta_notice_ack.UserBetaNoticeAckCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
	}
}

// Accepting stamps the CURRENT revision (never a client-supplied one) and
// appends exactly one audit event.
func TestAcknowledgeBetaNoticeStampsCurrentVersion(t *testing.T) {
	u := verifiedUser("beta@example.com")
	svc, repo, audit := newTestServiceWithAudit(u)

	updated, err := svc.AcknowledgeBetaNotice(context.Background(), ackCmd(u))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasAcknowledgedBetaNotice() {
		t.Fatal("acknowledgement did not take effect")
	}
	if updated.BetaNoticeAckVersion != user.BetaNoticeVersion {
		t.Fatalf("stamped version %d, want %d", updated.BetaNoticeAckVersion, user.BetaNoticeVersion)
	}
	if stored := repo.users[u.ID]; stored.BetaNoticeAckAt == nil {
		t.Fatal("acknowledgement was not persisted")
	}
	if len(audit.events) != 1 || audit.events[0].Action != "beta_notice.acknowledge" {
		t.Fatalf("expected one beta_notice.acknowledge event, got %+v", audit.events)
	}
}

// The audit event IS the consent record: if it cannot be written, the
// acknowledgement must not be stored either — a stamp with no event is the
// evidence gap the endpoint exists to close.
func TestAcknowledgeBetaNoticeAbortsWhenAuditFails(t *testing.T) {
	u := verifiedUser("beta@example.com")
	svc, repo, audit := newTestServiceWithAudit(u)
	audit.err = context.Canceled

	if _, err := svc.AcknowledgeBetaNotice(context.Background(), ackCmd(u)); err == nil {
		t.Fatal("expected the acknowledgement to fail when the audit write fails")
	}
	if stored := repo.users[u.ID]; stored.BetaNoticeAckAt != nil {
		t.Fatal("acknowledgement must not be persisted without an audit event")
	}
}

// Someone else's user ID is refused even with a valid session.
func TestAcknowledgeBetaNoticeRejectsOtherUser(t *testing.T) {
	u := verifiedUser("beta@example.com")
	svc, _, _ := newTestServiceWithAudit(u)

	cmd := ackCmd(u)
	cmd.ID = uuid.New()
	if _, err := svc.AcknowledgeBetaNotice(context.Background(), cmd); err == nil {
		t.Fatal("expected acknowledging on behalf of another user to fail")
	}
}

// A stamp against a superseded revision does not count as acknowledged — that
// is what lets a reworded notice re-prompt everyone.
func TestStaleAcknowledgementDoesNotCount(t *testing.T) {
	now := time.Now()
	u := &user.User{
		ID:                   uuid.New(),
		BetaNoticeAckAt:      &now,
		BetaNoticeAckVersion: user.BetaNoticeVersion - 1,
	}
	if u.HasAcknowledgedBetaNotice() {
		t.Fatal("an acknowledgement of an older revision must not count")
	}
}

// fakeNotifier records notification sends and can be told to fail.
type fakeNotifier struct {
	sent []struct{ to, subject, body string }
	err  error
}

func (f *fakeNotifier) Send(_ context.Context, to, subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

// The OLD address must be told its sign-in identifier was repointed — it is
// the mailbox an evicted owner still reads (password reset resolves by the NEW
// address, so nothing else reaches them). The notice names the new address.
func TestUpdateMeNotifiesPreviousAddressOnEmailChange(t *testing.T) {
	u := verifiedUser("old@example.com")
	svc, _ := newTestService(u)
	notifier := &fakeNotifier{}
	svc.SetChangeNotifier(notifier)

	if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected exactly one notice, got %d", len(notifier.sent))
	}
	if notifier.sent[0].to != "old@example.com" {
		t.Fatalf("notice must go to the PREVIOUS address, went to %q", notifier.sent[0].to)
	}
	if !strings.Contains(notifier.sent[0].body, "new@example.com") {
		t.Fatal("notice must name the new address so the owner can tell their own edit from a takeover")
	}
}

// A case-only change of the same address is not a repoint — no notice.
func TestUpdateMeCaseOnlyChangeSendsNoNotice(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, _ := newTestService(u)
	notifier := &fakeNotifier{}
	svc.SetChangeNotifier(notifier)

	if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "Owner@Example.COM"), liveSession()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("case-only change must not mail the old address, got %d notices", len(notifier.sent))
	}
}

// The notice is best-effort: a mail failure must not fail the update the user
// already made (same contract as the verification mail).
func TestUpdateMeNotifierFailureDoesNotFailTheUpdate(t *testing.T) {
	u := verifiedUser("old@example.com")
	svc, repo := newTestService(u)
	svc.SetChangeNotifier(&fakeNotifier{err: errors.New("smtp down")})

	updated, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession())
	if err != nil {
		t.Fatalf("a notification failure must not fail the update: %v", err)
	}
	if updated.Email != "new@example.com" || repo.users[u.ID].Email != "new@example.com" {
		t.Fatal("update must persist despite the failed notice")
	}
}

// THE regression: a PUT body that omits "email" must not blank the login
// identifier. This endpoint used to be a full replace, so a client sending only
// a name wrote "" over users.email — and since both sign-in and password reset
// resolve by email, the user could never get back in.
func TestUpdateMeOmittedEmailLeavesItAlone(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)

	name := "Renamed"
	updated, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Name:        &name,
	}, liveSession())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Email != "owner@example.com" {
		t.Fatalf("omitting email must leave it unchanged, got %q", updated.Email)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("name should have been updated, got %q", updated.Name)
	}
	if repo.users[u.ID].Email != "owner@example.com" {
		t.Fatalf("persisted email was blanked: %q", repo.users[u.ID].Email)
	}
	// The address never changed, so proven ownership still holds.
	if updated.EmailVerifiedAt == nil {
		t.Fatal("an unchanged email must keep its verification stamp")
	}
}

// Omitting the name must not blank it either — the same bug, other field.
func TestUpdateMeOmittedNameLeavesItAlone(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, _ := newTestService(u)

	email := "new@example.com"
	updated, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Email:       &email,
	}, liveSession())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Test" {
		t.Fatalf("omitting name must leave it unchanged, got %q", updated.Name)
	}
}

// An explicitly empty email is a blanking attempt, not "unchanged": the way to
// say "leave it alone" is to omit the key.
func TestUpdateMeExplicitEmptyEmailIsRefused(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)

	for _, blank := range []string{"", "   "} {
		email := blank
		_, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
			ID:          u.ID,
			AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
			Email:       &email,
		}, liveSession())
		if !errors.Is(err, app_errors.ErrUserEmailRequired) {
			t.Fatalf("blanking with %q must be refused, got %v", blank, err)
		}
		if repo.users[u.ID].Email != "owner@example.com" {
			t.Fatalf("stored email must be untouched, got %q", repo.users[u.ID].Email)
		}
	}
}

// An unparseable address would be a login identifier its owner can never prove
// or recover, so it is refused rather than stored.
func TestUpdateMeRejectsUnusableEmail(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)

	for _, bad := range []string{"not-an-email", "no@domain", "a b@example.com", "@example.com"} {
		email := bad
		_, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
			ID:          u.ID,
			AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
			Email:       &email,
		}, liveSession())
		if !errors.Is(err, app_errors.ErrUserEmailInvalid) {
			t.Fatalf("%q must be refused as invalid, got %v", bad, err)
		}
		if repo.users[u.ID].Email != "owner@example.com" {
			t.Fatalf("stored email must be untouched after rejecting %q", bad)
		}
	}
}

// The change-password path was the one of the four password-setting paths that
// never enforced user.MinPasswordLength, despite the constant's doc claiming it
// did. The browser's minLength={8} was the entire floor.
func TestUpdatePasswordEnforcesTheLengthFloor(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, _ := newTestService(u)

	_, err := svc.UpdatePassword(context.Background(), user_password_update.UserUpdatePasswordCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Password:    "x",
	}, liveSession())
	if !errors.Is(err, app_errors.ErrUserPasswordTooShort) {
		t.Fatalf("a one-character password must be refused, got %v", err)
	}

	_, err = svc.UpdatePassword(context.Background(), user_password_update.UserUpdatePasswordCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Password:    "",
	}, liveSession())
	if !errors.Is(err, app_errors.ErrUserPasswordRequired) {
		t.Fatalf("an empty password must be refused, got %v", err)
	}
}

// Past the ceiling bcrypt refuses to hash, which used to surface as a 500.
// 42 Cyrillic letters are 84 bytes: the ceiling is in bytes.
func TestUpdatePasswordEnforcesTheLengthCeiling(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, _ := newTestService(u)

	_, err := svc.UpdatePassword(context.Background(), user_password_update.UserUpdatePasswordCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Password:    strings.Repeat("Ж", 42),
	}, liveSession())
	if !errors.Is(err, app_errors.ErrUserPasswordTooLong) {
		t.Fatalf("an 84-byte password must be refused as too long, got %v", err)
	}
}

// The column-specific transitions, on the in-memory copy.
func (f *fakeUserRepo) SetPassword(_ context.Context, id uuid.UUID, hashed string, at time.Time) error {
	u, ok := f.users[id]
	if !ok {
		return app_errors.ErrUserNotFound
	}
	u.HashedPassword, u.PasswordChangedAt, u.CredentialGeneration = hashed, &at, u.CredentialGeneration+1
	return nil
}

// UpdateProfile mirrors the SQL statement: an omitted field is left as
// stored, and "changed" is decided against the STORED address here, never
// taken from the caller.
func (f *fakeUserRepo) UpdateProfile(_ context.Context, id uuid.UUID, name, email *string) (repository.ProfileChange, error) {
	u, ok := f.users[id]
	if !ok {
		return repository.ProfileChange{}, app_errors.ErrUserNotFound
	}
	change := repository.ProfileChange{PreviousEmail: u.Email}
	if name != nil {
		u.Name = *name
	}
	if email != nil {
		normalized := user.NormalizeEmail(*email)
		if normalized != u.Email {
			change.EmailChanged = true
			u.EmailVerifiedAt = nil
			u.CredentialGeneration++
		}
		u.Email = normalized
	}
	return change, nil
}

// The session-bound variants record what they were
// handed — the proof AND the partial-update arguments — and refuse the way
// the SQL boundary does: no proof, or a session marked dead, writes nothing.
type recordedWrite struct {
	name, email *string
	proof       sessionArgs
}

type sessionArgs struct {
	familyID   uuid.UUID
	issuedAt   time.Time
	generation int64
}

func (a sessionArgs) refused(dead bool) bool {
	return a.familyID == uuid.Nil || a.issuedAt.IsZero() || dead
}

func (f *fakeUserRepo) UpdateProfileWhileSessionLive(ctx context.Context, id uuid.UUID, name, email *string, familyID uuid.UUID, issuedAt time.Time, generation int64) (repository.ProfileChange, error) {
	args := sessionArgs{familyID, issuedAt, generation}
	f.lastProfileWrite = &recordedWrite{name: name, email: email, proof: args}
	if args.refused(f.sessionDead) {
		return repository.ProfileChange{}, app_errors.ErrTokenInvalid
	}
	return f.UpdateProfile(ctx, id, name, email)
}

func (f *fakeUserRepo) SetPasswordWhileSessionLive(ctx context.Context, id uuid.UUID, hashed string, at time.Time, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	args := sessionArgs{familyID, issuedAt, generation}
	f.lastPasswordProof = &args
	if args.refused(f.sessionDead) {
		return app_errors.ErrTokenInvalid
	}
	return f.SetPassword(ctx, id, hashed, at)
}

func (f *fakeUserRepo) SetPasswordIfEmail(ctx context.Context, id uuid.UUID, hashed, expectedEmail string, at time.Time) error {
	u, ok := f.users[id]
	if !ok {
		return app_errors.ErrUserNotFound
	}
	if user.NormalizeEmail(u.Email) != user.NormalizeEmail(expectedEmail) {
		return app_errors.ErrPasswordResetStale
	}
	return f.SetPassword(ctx, id, hashed, at)
}

func (f *fakeUserRepo) MarkEmailVerified(_ context.Context, id uuid.UUID, email string, at time.Time) (bool, error) {
	u, ok := f.users[id]
	if !ok || u.EmailVerifiedAt != nil || user.NormalizeEmail(u.Email) != user.NormalizeEmail(email) {
		return false, nil
	}
	u.EmailVerifiedAt = &at
	return true, nil
}

func (f *fakeUserRepo) AcknowledgeBetaNotice(_ context.Context, id uuid.UUID, version int, at time.Time) error {
	u, ok := f.users[id]
	if !ok {
		return app_errors.ErrUserNotFound
	}
	u.BetaNoticeAckAt, u.BetaNoticeAckVersion = &at, version
	return nil
}

type fakeVerifier struct{ err error }

func (v fakeVerifier) SendFor(context.Context, *user.User) error { return v.err }

// An install with no mail delivery configured changes an address without a
// warning; a real delivery failure still warns.
func TestUpdateMeWarnsOnlyForARealVerificationFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		warn bool
	}{
		{app_errors.ErrEmailNotConfigured, false},
		{errors.New("smtp down"), true},
	} {
		u := verifiedUser("old@example.com")
		svc, _ := newTestService(u)
		var buf strings.Builder
		svc.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		svc.SetEmailVerifier(fakeVerifier{err: tc.err})
		if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(buf.String(), "level=WARN"); got != tc.warn {
			t.Errorf("verifier error %v: warned=%t, want %t\n%s", tc.err, got, tc.warn, buf.String())
		}
	}
}
