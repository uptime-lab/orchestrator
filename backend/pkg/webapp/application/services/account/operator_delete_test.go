package account

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// The instance operator's account is derived (the oldest account), so
// deleting it would hand the operator role — the entitlement's terms and the
// right to apply a file — to the next-oldest account, a tenant. The delete is
// refused with a typed 409, nothing is deleted and nothing is audited as
// deleted.
func TestDeleteRefusesTheInstanceOperatorsAccount(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	svc, sink := auditAccountService(accountID)
	repo := svc.accountRepo.(*auditAccountRepo)
	repo.operator = accountID

	err := svc.DeleteAccount(context.Background(), account_delete.AccountDeleteCommand{
		ID: accountID, AuthContext: commands.NewAuthContext(accountID, userID)})
	var refusal *InstanceOperatorAccountError
	if !errors.As(err, &refusal) {
		t.Fatalf("deleting the operator's account: err = %v, want InstanceOperatorAccountError", err)
	}
	var facing results.OperatorFacingError
	if !errors.As(err, &facing) || facing.HTTPStatus() != http.StatusConflict || facing.ErrorCode() != "instance_operator_account" {
		t.Errorf("the refusal is not an operator-facing 409 instance_operator_account: %v", err)
	}
	if _, ok := repo.accounts[accountID]; !ok {
		t.Error("the operator's account row was deleted")
	}
	if _, ok := sink.Find(audit_event.ActionAccountDelete); ok {
		t.Error("a refused delete recorded account.delete")
	}
}

// A lookup that fails refuses the delete: a delete cannot be taken back,
// and a refused one can be retried.
func TestDeleteFailsClosedWhenTheOperatorCannotBeResolved(t *testing.T) {
	accountID := uuid.New()
	svc, sink := auditAccountService(accountID)
	repo := svc.accountRepo.(*auditAccountRepo)
	repo.operatorErr = errors.New("database is unhappy")

	if err := svc.DeleteAccount(context.Background(), account_delete.AccountDeleteCommand{
		ID: accountID, AuthContext: commands.NewAuthContext(accountID, uuid.New())}); err == nil {
		t.Fatal("a delete went ahead with the operator unresolved")
	}
	if _, ok := repo.accounts[accountID]; !ok {
		t.Error("the account row was deleted with the operator unresolved")
	}
	if _, ok := sink.Find(audit_event.ActionAccountDelete); ok {
		t.Error("a refused delete recorded account.delete")
	}
}
