package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	email_verification_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/email_verification"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// The registration* fakes accept the writes a sign-up makes on an empty
// install; anything else panics through the nil interface.
type registrationAccounts struct{ repository.AccountRepository }

func (registrationAccounts) CreateAdmitted(_ context.Context, a *account.Account, _ time.Time, admit repository.AccountAdmission) error {
	return admit(repository.AccountCounts{}, a)
}

type registrationUsers struct{ repository.UserRepository }

func (registrationUsers) Create(context.Context, *user.User) error { return nil }

type registrationMembers struct {
	repository.AccountMemberRepository
}

func (registrationMembers) Create(context.Context, *account_member.AccountMember) error { return nil }
func (registrationMembers) ListByUserID(context.Context, uuid.UUID) ([]account_member.AccountMember, error) {
	return nil, nil
}

type registrationBindings struct {
	repository.RoleBindingRepository
}

func (registrationBindings) Create(context.Context, *role_binding.RoleBinding) error { return nil }

type failingVerifier struct{ err error }

func (v failingVerifier) SendFor(context.Context, *user.User) error { return v.err }
func (v failingVerifier) Enabled() bool                             { return true }

// signupLog registers the first owner of an empty install with the given
// verifier and returns everything the registration logged.
func signupLog(t *testing.T, verifier emailVerifier) string {
	t.Helper()
	var buf bytes.Buffer
	svc := admissionService(&countingAccountRepo{}, 0, "")
	svc.accountRepo = registrationAccounts{}
	svc.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc.userRepo = registrationUsers{}
	svc.memberRepo = registrationMembers{}
	svc.bindingRepo = registrationBindings{}
	svc.SetEmailVerifier(verifier)
	// The fakes list no membership, so the token step after the mail fails;
	// the verification mail has been attempted by then, which is all this
	// reads.
	_, _, _, _ = svc.registerOwner(context.Background(), "owner@example.com", "Owner", "Acme", "hash", svc.admitSignup(""))
	return buf.String()
}

// An install with no mail delivery configured registers its owners without a
// warning: the missing sender is the install's shape, not a failed send.
func TestRegistrationWithoutAMailSenderLogsNoWarning(t *testing.T) {
	unconfigured := email_verification_service.NewService(
		struct {
			repository.EmailVerificationRepository
		}{}, registrationUsers{}, nil, "", slog.New(slog.DiscardHandler))
	out := signupLog(t, unconfigured)
	if strings.Contains(out, "level=WARN") {
		t.Fatalf("registration without a mail sender warned:\n%s", out)
	}
	if !strings.Contains(out, "email delivery is not configured") {
		t.Errorf("the skipped mail is not recorded at debug:\n%s", out)
	}
}

// A real delivery failure still warns.
func TestRegistrationWarnsWhenTheVerificationMailFails(t *testing.T) {
	out := signupLog(t, failingVerifier{err: errors.New("smtp: connection refused")})
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "failed to send signup verification email") {
		t.Fatalf("a failed verification mail did not warn:\n%s", out)
	}
}
