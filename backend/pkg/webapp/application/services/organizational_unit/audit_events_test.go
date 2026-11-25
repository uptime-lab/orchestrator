package organizational_unit

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// An OU update that flips ssh_password_auth dispatches apply_access_config to
// the fleet; the audit row is the only durable record that a human did it. It
// must name the actor, the pushed-node count — and never the password value.
func TestOUUpdateRecordsAuditWithPushCount(t *testing.T) {
	f := newFleetFixture(nil, nil)
	recorder, auditRepo := testAuditRecorder()
	f.svc.audit = recorder
	actor := uuid.New()

	flag := organizational_unit.PolicyOn
	secretValue := "hunter2-super-secret"
	_, err := f.svc.Update(context.Background(), organizational_unit_update.OrganizationalUnitUpdateCommand{
		ID:              f.rootID,
		SSHPasswordAuth: &flag,
		DefaultPassword: &secretValue,
		AuthContext:     commands.NewAuthContext(f.accountID, actor),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	events := auditRepo.recorded()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	e := events[0]
	if e.Action != audit_event.ActionOUUpdate {
		t.Errorf("action = %q, want %q", e.Action, audit_event.ActionOUUpdate)
	}
	if e.ActorUserID == nil || *e.ActorUserID != actor {
		t.Errorf("actor not recorded: %v", e.ActorUserID)
	}
	if !strings.Contains(e.Detail, "push=ssh_password_auth") || !strings.Contains(e.Detail, "nodes=") {
		t.Errorf("detail must carry the fleet-push reason and node count, got %q", e.Detail)
	}
	if !strings.Contains(e.Detail, "default_password") {
		t.Errorf("detail must name the touched field, got %q", e.Detail)
	}
	if strings.Contains(e.Detail, secretValue) {
		t.Fatalf("detail leaked a password value: %q", e.Detail)
	}
}

// The account policy update is the other fleet-reaching write; same contract.
func TestAccountPolicyUpdateRecordsAudit(t *testing.T) {
	f := newFleetFixture(nil, nil)
	recorder, auditRepo := testAuditRecorder()
	f.svc.audit = recorder

	flag := organizational_unit.PolicyOff
	_, err := f.svc.SetAccountPolicy(context.Background(), account_policy_update.AccountPolicyUpdateCommand{
		SSHPasswordAuth: &flag,
		AuthContext:     commands.NewAuthContext(f.accountID, uuid.New()),
	})
	if err != nil {
		t.Fatalf("SetAccountPolicy: %v", err)
	}

	events := auditRepo.recorded()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	e := events[0]
	if e.Action != audit_event.ActionAccountPolicyUpdate {
		t.Errorf("action = %q, want %q", e.Action, audit_event.ActionAccountPolicyUpdate)
	}
	if !strings.Contains(e.Detail, "push=ssh_password_auth") {
		t.Errorf("detail must carry the fleet-push clause, got %q", e.Detail)
	}
}
