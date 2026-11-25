package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// A stored provisioning password no configured key can decrypt is
// deliberately preserved by the repository when the caller does not touch the
// field — a rotation restoring the right key can still recover it. But the
// same guard used to fire when the caller EXPLICITLY cleared the password: the
// service left DefaultPasswordUnreadable set, the repository preserved the
// ciphertext, and the clear answered 200 while changing nothing. The service
// must drop the flag whenever the field is explicitly supplied.

type unreadablePolicyRepo struct {
	repository.AccountPolicyRepository
	stored   *organizational_unit.AccountPolicy
	upserted *organizational_unit.AccountPolicy
}

func (r *unreadablePolicyRepo) Get(context.Context, uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	copied := *r.stored
	return &copied, nil
}

func (r *unreadablePolicyRepo) Upsert(_ context.Context, policy *organizational_unit.AccountPolicy) error {
	copied := *policy
	r.upserted = &copied
	return nil
}

func TestExplicitClearOfAnUnreadablePasswordStandsDownThePreserveGuard(t *testing.T) {
	accountID := uuid.New()
	repo := &unreadablePolicyRepo{stored: &organizational_unit.AccountPolicy{
		AccountID:                 accountID,
		DefaultPasswordUnreadable: true,
	}}
	recorder, _ := testAuditRecorder()
	svc := &Service{
		accountPolicyRepo: repo,
		audit:             recorder,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	empty := ""
	_, err := svc.SetAccountPolicy(context.Background(), account_policy_update.AccountPolicyUpdateCommand{
		DefaultPassword: &empty,
		AuthContext:     commands.NewAuthContext(accountID, uuid.New()),
	})
	if err != nil {
		t.Fatalf("SetAccountPolicy: %v", err)
	}

	if repo.upserted == nil {
		t.Fatal("policy was never upserted")
	}
	if repo.upserted.DefaultPasswordUnreadable {
		t.Fatal("explicit clear must drop the unreadable flag, or the repository preserves the dead ciphertext and the clear no-ops")
	}
	if repo.upserted.DefaultPassword != nil {
		t.Fatalf("cleared password should be nil, got %q", *repo.upserted.DefaultPassword)
	}
}

// The other half of the contract must survive: a caller who does NOT touch the
// field keeps the preserved ciphertext recoverable.
func TestAnUntouchedUnreadablePasswordKeepsThePreserveGuard(t *testing.T) {
	accountID := uuid.New()
	repo := &unreadablePolicyRepo{stored: &organizational_unit.AccountPolicy{
		AccountID:                 accountID,
		DefaultPasswordUnreadable: true,
	}}
	recorder, _ := testAuditRecorder()
	svc := &Service{
		accountPolicyRepo: repo,
		audit:             recorder,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	username := "pi"
	_, err := svc.SetAccountPolicy(context.Background(), account_policy_update.AccountPolicyUpdateCommand{
		DefaultUsername: &username,
		AuthContext:     commands.NewAuthContext(accountID, uuid.New()),
	})
	if err != nil {
		t.Fatalf("SetAccountPolicy: %v", err)
	}

	if repo.upserted == nil {
		t.Fatal("policy was never upserted")
	}
	if !repo.upserted.DefaultPasswordUnreadable {
		t.Fatal("an update that does not touch the password must keep the preserve guard armed")
	}
}
