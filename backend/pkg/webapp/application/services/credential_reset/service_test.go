package credential_reset

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The fakes embed the port so only what the service calls is implemented;
// anything else panics, which is the point — the service must not reach for
// a whole-row Update or any other write.

type fakeUsers struct {
	repository.UserRepository
	found       []*user.User
	setPassword []string
}

func (f *fakeUsers) ListByEmailIgnoringCase(_ context.Context, _ string) ([]*user.User, error) {
	return f.found, nil
}

func (f *fakeUsers) SetPassword(_ context.Context, _ uuid.UUID, hashed string, _ time.Time) error {
	f.setPassword = append(f.setPassword, hashed)
	return nil
}

type fakeFactors struct {
	repository.TwoFactorRepository
	enabled bool
	calls   int
}

func (f *fakeFactors) RemoveEnabledAndAdvanceGeneration(_ context.Context, _ uuid.UUID) (bool, error) {
	f.calls++
	was := f.enabled
	f.enabled = false
	return was, nil
}

type fakeSessions struct {
	repository.RefreshTokenRepository
	revoked int
	err     error
}

func (f *fakeSessions) RevokeAllForUser(_ context.Context, _ uuid.UUID) error {
	f.revoked++
	return f.err
}

type fakeResets struct {
	repository.PasswordResetRepository
	revoked int
}

func (f *fakeResets) RevokeUnusedByUserID(_ context.Context, _ uuid.UUID) error {
	f.revoked++
	return nil
}

type fakeMembers struct {
	repository.AccountMemberRepository
	accounts []uuid.UUID
	disabled []uuid.UUID
	err      error
}

func (f *fakeMembers) ListByUserID(_ context.Context, userID uuid.UUID) ([]account_member.AccountMember, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]account_member.AccountMember, 0, len(f.accounts)+len(f.disabled))
	for _, a := range f.accounts {
		out = append(out, account_member.AccountMember{AccountID: a, UserID: userID, Status: account_member.StatusActive})
	}
	for _, a := range f.disabled {
		out = append(out, account_member.AccountMember{AccountID: a, UserID: userID, Status: account_member.StatusDisabled})
	}
	return out, nil
}

type fixture struct {
	svc      *Service
	users    *fakeUsers
	factors  *fakeFactors
	sessions *fakeSessions
	resets   *fakeResets
	sink     *audittest.Sink
	members  *fakeMembers
}

func newFixture(found []*user.User, accounts ...uuid.UUID) *fixture {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{
		users: &fakeUsers{found: found}, factors: &fakeFactors{}, sessions: &fakeSessions{},
		resets: &fakeResets{}, sink: &audittest.Sink{}, members: &fakeMembers{accounts: accounts},
	}
	f.svc = NewService(f.users, f.factors, f.sessions, f.resets, f.members,
		services.NewBCryptPasswordHasher(4), audit.NewRecorder(f.sink, logger), logger)
	return f
}

func someUser() *user.User {
	return user.NewUser("owner@example.com", "Owner", "old-hash")
}

func TestResetRefusesAnUnknownAddress(t *testing.T) {
	f := newFixture(nil)
	if _, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "x@example.com"}); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("password: %v", err)
	}
	if _, err := f.svc.ResetTOTP(context.Background(), cmd.ResetTOTPCommand{Email: "x@example.com"}); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("totp: %v", err)
	}
	if len(f.users.setPassword) != 0 || f.factors.calls != 0 || f.sessions.revoked != 0 || len(f.sink.Events()) != 0 {
		t.Fatal("a refusal wrote something")
	}
}

// Two identities answering to one address is a refusal, never "the first".
func TestResetRefusesAnAmbiguousAddress(t *testing.T) {
	f := newFixture([]*user.User{someUser(), someUser()})
	if _, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com"}); !errors.Is(err, ErrAmbiguousEmail) {
		t.Fatalf("password: %v", err)
	}
	if _, err := f.svc.ResetTOTP(context.Background(), cmd.ResetTOTPCommand{Email: "owner@example.com"}); !errors.Is(err, ErrAmbiguousEmail) {
		t.Fatalf("totp: %v", err)
	}
	if len(f.users.setPassword) != 0 || f.factors.calls != 0 || f.sessions.revoked != 0 {
		t.Fatal("an ambiguous address wrote something")
	}
}

func TestResetRefusesInvalidCommands(t *testing.T) {
	f := newFixture([]*user.User{someUser()})
	for _, c := range []cmd.ResetPasswordCommand{{Email: ""}, {Email: "not-an-address"}, {Email: "owner@example.com", Password: "short"}} {
		if _, err := f.svc.ResetPassword(context.Background(), c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if len(f.users.setPassword) != 0 {
		t.Fatal("an invalid command wrote a password")
	}
}

// A TOTP reset for a user with no enabled authenticator changes nothing:
// no session is ended and nothing is audited.
func TestResetTOTPWithoutAnAuthenticatorChangesNothing(t *testing.T) {
	f := newFixture([]*user.User{someUser()}, uuid.New())
	if _, err := f.svc.ResetTOTP(context.Background(), cmd.ResetTOTPCommand{Email: "owner@example.com"}); !errors.Is(err, ErrNoEnabledAuthenticator) {
		t.Fatalf("err = %v", err)
	}
	if f.sessions.revoked != 0 || len(f.sink.Events()) != 0 {
		t.Fatal("a refused TOTP reset ended sessions or wrote an audit row")
	}
}

// The generated password is returned once, is long, is what was hashed,
// and appears in no audit row; every account of the user gets a system row.
func TestResetPasswordGeneratesRevokesAndAudits(t *testing.T) {
	a1, a2 := uuid.New(), uuid.New()
	usr := someUser()
	f := newFixture([]*user.User{usr}, a1, a2)
	res, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "OWNER@example.com "})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.TemporaryPassword) != GeneratedPasswordLength || res.UserID != usr.ID {
		t.Fatalf("result = %+v", res)
	}
	if len(f.users.setPassword) != 1 || services.NewBCryptPasswordHasher(4).ComparePasswords(f.users.setPassword[0], res.TemporaryPassword) != nil {
		t.Fatal("the stored hash is not the hash of the returned password")
	}
	if !res.SessionsRevoked || f.sessions.revoked != 1 || f.resets.revoked != 1 {
		t.Fatalf("sessions=%d resets=%d", f.sessions.revoked, f.resets.revoked)
	}
	events := f.sink.Events()
	if len(events) != 2 || res.AuditedAccounts != 2 {
		t.Fatalf("want one row per account, got %d", len(events))
	}
	for _, e := range events {
		if e.Action != audit_event.ActionUserPasswordResetByOperator || e.TargetType != audit_event.TargetUser || e.TargetID != usr.ID.String() {
			t.Errorf("row = %+v", e)
		}
		if e.ActorUserID != nil {
			t.Errorf("the row must be the system actor: %+v", e)
		}
		if strings.Contains(e.Detail, res.TemporaryPassword) || strings.Contains(e.Detail, f.users.setPassword[0]) {
			t.Fatalf("the audit row carries credential material: %q", e.Detail)
		}
	}
}

// A chosen password is stored and never returned.
func TestResetPasswordWithAChosenPasswordReturnsNone(t *testing.T) {
	f := newFixture([]*user.User{someUser()}, uuid.New())
	res, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com", Password: "a-chosen-password"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TemporaryPassword != "" {
		t.Fatal("a chosen password must not be echoed back")
	}
	if services.NewBCryptPasswordHasher(4).ComparePasswords(f.users.setPassword[0], "a-chosen-password") != nil {
		t.Fatal("the chosen password was not the one stored")
	}
}

// The password is already written when the revocation fails; the result
// says so instead of reporting the whole reset as failed.
func TestResetPasswordReportsAFailedRevocation(t *testing.T) {
	f := newFixture([]*user.User{someUser()}, uuid.New())
	f.sessions.err = errors.New("pool timeout")
	res, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsRevoked || res.TemporaryPassword == "" {
		t.Fatalf("result = %+v", res)
	}
	if e, ok := f.sink.Find(audit_event.ActionUserPasswordResetByOperator); !ok || !strings.Contains(e.Detail, "sessions_revoked=false") {
		t.Fatalf("the audit row must say the revocation failed: %+v", e)
	}
}

func TestResetTOTPRemovesRevokesAndAudits(t *testing.T) {
	usr := someUser()
	f := newFixture([]*user.User{usr}, uuid.New())
	f.factors.enabled = true
	res, err := f.svc.ResetTOTP(context.Background(), cmd.ResetTOTPCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TemporaryPassword != "" || !res.SessionsRevoked || f.sessions.revoked != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := f.sink.Find(audit_event.ActionUserTwoFactorResetByOperator); !ok {
		t.Fatal("no audit row")
	}
	if len(f.users.setPassword) != 0 {
		t.Fatal("a TOTP reset touched the password")
	}
}

func TestGeneratedPasswordsDiffer(t *testing.T) {
	a, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GeneratePassword()
	if a == b || len(a) < user.MinPasswordLength {
		t.Fatalf("%q %q", a, b)
	}
	for _, r := range a {
		if !strings.ContainsRune(passwordAlphabet, r) {
			t.Fatalf("%q outside the alphabet", r)
		}
	}
}

// A disabled member is still a member: their account records the change.
// (A removed member has no row at all — removal is a DELETE.)
func TestResetRecordsUnderDisabledMemberships(t *testing.T) {
	active, disabled := uuid.New(), uuid.New()
	f := newFixture([]*user.User{someUser()}, active)
	f.members.disabled = []uuid.UUID{disabled}
	res, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]bool{}
	for _, e := range f.sink.Events() {
		got[e.AccountID] = true
	}
	if res.AuditedAccounts != 2 || !got[active] || !got[disabled] {
		t.Fatalf("audited=%d rows under %v, want both the active and the disabled account", res.AuditedAccounts, got)
	}
}

// No membership and an unreadable membership list both record nothing, and
// the result tells the two apart so the command line can say which.
func TestResetWithoutAnAuditTrailIsReportedNotHidden(t *testing.T) {
	f := newFixture([]*user.User{someUser()})
	res, err := f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuditedAccounts != 0 || res.MembershipsUnreadable || len(f.sink.Events()) != 0 {
		t.Fatalf("no membership: %+v, %d rows", res, len(f.sink.Events()))
	}

	f = newFixture([]*user.User{someUser()}, uuid.New())
	f.members.err = errors.New("pool timeout")
	res, err = f.svc.ResetPassword(context.Background(), cmd.ResetPasswordCommand{Email: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuditedAccounts != 0 || !res.MembershipsUnreadable {
		t.Fatalf("unreadable memberships: %+v", res)
	}
}
