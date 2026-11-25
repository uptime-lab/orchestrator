package authz

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// A role setting is a privilege change for EVERY member holding the role, not
// for one person. The endpoint already demands a step-up; and it must leave a
// record of who used it.

type auditRoleSettingRepo struct {
	repository.AccountRoleSettingRepository
	upserted []account_role_setting.AccountRoleSetting
	deleted  []string
}

func (r *auditRoleSettingRepo) Upsert(_ context.Context, s *account_role_setting.AccountRoleSetting) error {
	r.upserted = append(r.upserted, *s)
	return nil
}

func (r *auditRoleSettingRepo) Delete(_ context.Context, _ uuid.UUID, role, action string) error {
	r.deleted = append(r.deleted, role+"|"+action)
	return nil
}

func auditEnforcer() (*Enforcer, *audittest.Sink) {
	recorder, sink := audittest.New()
	return NewEnforcer(nil, nil, &auditRoleSettingRepo{}, nil, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil))), sink
}

// A tunable action to exercise, taken from the live list so the test cannot pin
// an action that has since stopped being tunable.
func firstTunableAction(t *testing.T) authz.Action {
	t.Helper()
	actions := authz.TunableActionList()
	if len(actions) == 0 {
		t.Skip("no tunable actions declared")
	}
	return actions[0]
}

func TestUpsertRoleSettingIsAudited(t *testing.T) {
	enforcer, sink := auditEnforcer()
	accountID, actorUserID := uuid.New(), uuid.New()
	action := firstTunableAction(t)

	if err := enforcer.UpsertRoleSetting(context.Background(), accountID, actorUserID,
		authz.RoleManager, action, true); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionRoleSettingUpsert)
	if !ok {
		t.Fatalf("no role_setting.upsert event; recorded %v", sink.Actions())
	}
	if event.ActorUserID == nil || *event.ActorUserID != actorUserID {
		t.Errorf("actor = %v, want %v", event.ActorUserID, actorUserID)
	}
	// The target is the ROLE, not a user: the change affects everyone holding it.
	if event.TargetType != audit_event.TargetRole || event.TargetID != string(authz.RoleManager) {
		t.Errorf("target = %s/%s, want role/%s", event.TargetType, event.TargetID, authz.RoleManager)
	}
	for _, want := range []string{string(action), "allowed=true"} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

func TestResetRoleSettingIsAudited(t *testing.T) {
	enforcer, sink := auditEnforcer()
	accountID, actorUserID := uuid.New(), uuid.New()
	action := firstTunableAction(t)

	if err := enforcer.ResetRoleSetting(context.Background(), accountID, actorUserID,
		authz.RoleManager, action); err != nil {
		t.Fatalf("reset: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionRoleSettingReset)
	if !ok {
		t.Fatalf("no role_setting.reset event; recorded %v", sink.Actions())
	}
	if event.Detail != string(action) {
		t.Errorf("detail = %q, want the reset action %q", event.Detail, action)
	}
}

// Reverting a whole role has a different blast radius from reverting one action,
// so it gets its own action string rather than N indistinguishable reset rows.
func TestResetRoleIsAuditedAsOneRowWithItsOwnAction(t *testing.T) {
	enforcer, sink := auditEnforcer()

	if err := enforcer.ResetRole(context.Background(), uuid.New(), uuid.New(),
		authz.RoleManager); err != nil {
		t.Fatalf("reset role: %v", err)
	}

	actions := sink.Actions()
	if len(actions) != 1 {
		t.Fatalf("recorded %d rows for one revert: %v", len(actions), actions)
	}
	if actions[0] != audit_event.ActionRoleSettingResetRole {
		t.Errorf("action = %q, want %q", actions[0], audit_event.ActionRoleSettingResetRole)
	}
}

// A rejected change must not be recorded as one: super_admin is not tunable.
func TestARejectedRoleSettingChangeRecordsNothing(t *testing.T) {
	enforcer, sink := auditEnforcer()

	if err := enforcer.UpsertRoleSetting(context.Background(), uuid.New(), uuid.New(),
		authz.RoleSuperAdmin, firstTunableAction(t), true); err == nil {
		t.Fatal("expected super_admin to be rejected as untunable")
	}
	if actions := sink.Actions(); len(actions) != 0 {
		t.Errorf("a rejected change recorded %v", actions)
	}
}

func TestNewEnforcerRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewEnforcer(nil, nil, &auditRoleSettingRepo{}, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}
