package account

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

type auditAccountRepo struct {
	repository.AccountRepository
	accounts map[uuid.UUID]*account.Account
	// operator is the instance operator's account id (uuid.Nil: some other
	// account, never one of these); operatorErr makes the lookup fail.
	operator    uuid.UUID
	operatorErr error
}

func (r *auditAccountRepo) InstanceOperatorAccountID(context.Context) (uuid.UUID, error) {
	if r.operatorErr != nil {
		return uuid.Nil, r.operatorErr
	}
	if r.operator == uuid.Nil {
		return uuid.MustParse("00000000-0000-0000-0000-000000000001"), nil
	}
	return r.operator, nil
}

func (r *auditAccountRepo) GetByID(_ context.Context, id uuid.UUID) (*account.Account, error) {
	a, ok := r.accounts[id]
	if !ok {
		return nil, app_errors.ErrAccountNotFound
	}
	copied := *a
	return &copied, nil
}

func (r *auditAccountRepo) Update(_ context.Context, a *account.Account) error {
	copied := *a
	r.accounts[a.ID] = &copied
	return nil
}

func (r *auditAccountRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.accounts, id)
	return nil
}

// Each of these deletes one slice of the account's data; none of them matters to
// the audit assertions, only that they succeed. Declared separately rather than
// via a shared embedded helper, because embedding both the interface and a
// helper makes DeleteByAccountID an ambiguous selector.
type auditBindingRepo struct{ repository.RoleBindingRepository }

func (auditBindingRepo) DeleteByAccountID(context.Context, uuid.UUID) error { return nil }

type auditInviteRepo struct{ repository.AccountInviteRepository }

func (auditInviteRepo) DeleteByAccountID(context.Context, uuid.UUID) error { return nil }

type auditRoleSettingRepo struct{ repository.AccountRoleSettingRepository }

func (auditRoleSettingRepo) DeleteByAccountID(context.Context, uuid.UUID) error { return nil }

type auditMemberRepo struct{ repository.AccountMemberRepository }

func (auditMemberRepo) DeleteByAccountID(context.Context, uuid.UUID) error { return nil }

func auditAccountService(accountID uuid.UUID) (*Service, *audittest.Sink) {
	recorder, sink := audittest.New()
	repo := &auditAccountRepo{accounts: map[uuid.UUID]*account.Account{
		accountID: {ID: accountID, Name: "Acme"},
	}}
	return NewAccountService(repo, nil, auditMemberRepo{}, auditBindingRepo{},
		auditInviteRepo{}, auditRoleSettingRepo{}, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil))), sink
}

// require_two_factor is the field that matters: turning it OFF removes an
// enrollment requirement from every member at once, and the endpoint's own
// step-up guard leaves no record of who did it.
func TestAccountUpdateRecordsTheTwoFactorRequirement(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	svc, sink := auditAccountService(accountID)
	requireTwoFactor := false

	if _, err := svc.UpdateAccount(context.Background(), account_update.AccountUpdateCommand{
		ID:               accountID,
		Name:             strptr("Acme Corp"),
		RequireTwoFactor: &requireTwoFactor,
		AuthContext:      commands.NewAuthContext(accountID, userID),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionAccountUpdate)
	if !ok {
		t.Fatalf("no account.update event; recorded %v", sink.Actions())
	}
	if event.TargetType != audit_event.TargetAccount || event.TargetID != accountID.String() {
		t.Errorf("target = %s/%s, want account/%s", event.TargetType, event.TargetID, accountID)
	}
	for _, want := range []string{"Acme Corp", "require_two_factor=false"} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

// The row deliberately outlives the tenant: audit_events has no foreign key to
// accounts, so it survives as the only evidence a self-service teardown happened
// and who asked for it.
func TestAccountDeleteIsAudited(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	svc, sink := auditAccountService(accountID)

	if err := svc.DeleteAccount(context.Background(), account_delete.AccountDeleteCommand{
		ID:          accountID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionAccountDelete)
	if !ok {
		t.Fatalf("no account.delete event; recorded %v", sink.Actions())
	}
	if event.AccountID != accountID {
		t.Errorf("account = %v, want %v", event.AccountID, accountID)
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Errorf("actor = %v, want %v", event.ActorUserID, userID)
	}
}

// A cross-tenant attempt is refused before anything changes; it must not leave a
// row claiming a change was made.
func TestARefusedCrossTenantUpdateRecordsNothing(t *testing.T) {
	accountID := uuid.New()
	svc, sink := auditAccountService(accountID)

	if _, err := svc.UpdateAccount(context.Background(), account_update.AccountUpdateCommand{
		ID:          accountID,
		Name: strptr("Hijacked"),
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()), // another tenant
	}); err == nil {
		t.Fatal("expected a cross-tenant update to be refused")
	}
	if actions := sink.Actions(); len(actions) != 0 {
		t.Errorf("a refused update recorded %v", actions)
	}
}

func TestNewAccountServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewAccountService(&auditAccountRepo{}, nil, auditMemberRepo{}, auditBindingRepo{},
		auditInviteRepo{}, auditRoleSettingRepo{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func strptr(s string) *string { return &s }
