package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
)

// countingAccountRepo answers Count with a fixed number, or an error, and
// records whether Create was reached.
type countingAccountRepo struct {
	repository.AccountRepository
	accounts int64 // every account row
	pending  int64 // of those, pending open sign-ups inside the window
	err      error
	asked    int
	created  int
	// written is the last candidate the admission let through.
	written *account.Account
	// allowCreate lets the insert succeed; by default the test stops there.
	allowCreate bool
}

func (r *countingAccountRepo) Counts(_ context.Context, _ time.Time) (repository.AccountCounts, error) {
	r.asked++
	if r.err != nil {
		return repository.AccountCounts{}, r.err
	}
	return repository.AccountCounts{Total: r.accounts, Settled: r.accounts - r.pending, Pending: r.pending}, nil
}

// settledCounts is an instance of n settled accounts and nothing pending.
func settledCounts(n int64) repository.AccountCounts {
	return repository.AccountCounts{Total: n, Settled: n}
}

// admitWith asks an admission about an instance of n settled accounts with
// a throwaway candidate.
func admitWith(admit admission, n int64) error {
	return admit(settledCounts(n), &account.Account{})
}

func (r *countingAccountRepo) Create(_ context.Context, a *account.Account) error {
	r.created++
	r.written = a
	if r.allowCreate {
		return nil
	}
	return errors.New("stop here: the admission is what this test is about")
}

// CreateAdmitted mirrors the SQL implementation's contract without the
// lock: a count error is ErrAccountCountUnavailable and nothing is created;
// the admission's refusal is returned unchanged; otherwise Create.
func (r *countingAccountRepo) CreateAdmitted(ctx context.Context, a *account.Account, since time.Time, admit repository.AccountAdmission) error {
	counts, err := r.Counts(ctx, since)
	if err != nil {
		return fmt.Errorf("%w: %v", repository.ErrAccountCountUnavailable, err)
	}
	if err := admit(counts, a); err != nil {
		return err
	}
	return r.Create(ctx, a)
}

func admissionService(repo *countingAccountRepo, limit int, inviteCode string) *AuthService {
	return &AuthService{
		accountRepo: repo,
		maxAccounts: limit,
		inviteCode:  inviteCode,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func isClosed(err error, reason app_errors.RegistrationClosedReason) bool {
	var closed *app_errors.RegistrationClosedError
	return errors.As(err, &closed) && closed.Reason == reason && errors.Is(err, app_errors.ErrRegistrationClosed)
}

// An empty install belongs to whoever installed it: the first owner needs no
// code, whether or not one is configured.
func TestTheFirstOwnerIsAdmittedWithoutAnInviteCode(t *testing.T) {
	for _, configured := range []string{"", "a-code"} {
		s := admissionService(&countingAccountRepo{accounts: 0}, 1, configured)
		if err := admitWith(s.admitSignup(""), 0); err != nil {
			t.Fatalf("configured=%q: the first owner was refused: %v", configured, err)
		}
	}
}

// From the first account on, no configured code means CLOSED — never open.
func TestWithAccountsAndNoInviteCodeRegistrationIsClosed(t *testing.T) {
	s := admissionService(&countingAccountRepo{accounts: 1}, 0, "")
	err := admitWith(s.admitSignup("anything"), 1)
	if !isClosed(err, app_errors.ReasonNoInviteCode) {
		t.Fatalf("want closed (no invite code), got %v", err)
	}
}

func TestWithAccountsTheInviteCodeIsRequiredAndCompared(t *testing.T) {
	s := admissionService(&countingAccountRepo{accounts: 1}, 0, "a-code")
	if err := admitWith(s.admitSignup(""), 1); !errors.Is(err, app_errors.ErrInvalidInviteCode) {
		t.Fatalf("no code presented: want ErrInvalidInviteCode, got %v", err)
	}
	if err := admitWith(s.admitSignup("b-code"), 1); !errors.Is(err, app_errors.ErrInvalidInviteCode) {
		t.Fatalf("wrong code: want ErrInvalidInviteCode, got %v", err)
	}
	if err := admitWith(s.admitSignup("a-code"), 1); err != nil {
		t.Fatalf("right code: %v", err)
	}
}

// The invite code says WHO may register and nothing about how many times;
// this is the missing half. At the ceiling, no further account is created,
// and a full instance says "closed" even to a code that would have been right.
func TestRegistrationIsRefusedAtTheAccountCeiling(t *testing.T) {
	s := admissionService(&countingAccountRepo{}, 30, "a-code")
	if err := admitWith(s.admitSignup("a-code"), 30); !isClosed(err, app_errors.ReasonAccountCeiling) {
		t.Fatalf("expected the ceiling refusal at 30 of 30, got %v", err)
	}
	// One below it still lets someone in — an off-by-one here closes registration a
	// person early, which looks like a bug to whoever is holding the code.
	if err := admitWith(s.admitSignup("a-code"), 29); err != nil {
		t.Fatalf("29 of 30 must still be allowed to register: %v", err)
	}
	// The OIDC path asks the ceiling alone.
	if err := s.admitUnderCeiling(settledCounts(30), &account.Account{}); !isClosed(err, app_errors.ReasonAccountCeiling) {
		t.Fatalf("expected the ceiling refusal on the OIDC path, got %v", err)
	}
}

// 0 = unlimited, the same convention as every other guard here.
func TestAnUnsetCeilingIsUnlimited(t *testing.T) {
	s := admissionService(&countingAccountRepo{}, 0, "a-code")
	if err := admitWith(s.admitSignup("a-code"), 1_000_000); err != nil {
		t.Fatalf("an unlimited install refused a registration: %v", err)
	}
}

// The admission is asked at the one place an account is created, against the
// stored count, and a count that cannot be read REFUSES: the answer decides
// whether a code is required at all, so an unknown count would admit a
// stranger with none. A sign-up is the one admission that is safe to refuse
// on doubt.
func TestProvisionNewOwnerAsksTheAdmissionAndFailsClosedOnACountError(t *testing.T) {
	repo := &countingAccountRepo{accounts: 1}
	s := admissionService(repo, 0, "")
	_, err := s.provisionNewOwner(context.Background(), "a@example.com", "A", "Acc", "hash", s.admitSignup(""))
	if !isClosed(err, app_errors.ReasonNoInviteCode) {
		t.Fatalf("want closed, got %v", err)
	}
	if repo.created != 0 {
		t.Fatal("a refused admission must not reach the insert")
	}

	repo = &countingAccountRepo{err: errors.New("database is down")}
	s = admissionService(repo, 0, "a-code")
	_, err = s.provisionNewOwner(context.Background(), "a@example.com", "A", "Acc", "hash", s.admitSignup("a-code"))
	if !isClosed(err, app_errors.ReasonCountUnavailable) {
		t.Fatalf("a failed count must refuse the registration, got %v", err)
	}
	if repo.created != 0 {
		t.Fatal("a failed count must not reach the insert")
	}
}
