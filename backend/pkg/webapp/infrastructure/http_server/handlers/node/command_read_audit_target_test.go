package node

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodecommand "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

type singleCommandService struct {
	services.NodeCommandService
	cmd *nodecommand.NodeCommand
}

func (s singleCommandService) GetNodeCommandByID(_ context.Context, _, _ uuid.UUID) (*nodecommand.NodeCommand, error) {
	return s.cmd, nil
}

// Reading one command through /node-commands/:id serves its plaintext payload
// to a secret.read_value holder; the audit row for that read must name the
// command's NODE, the target a reviewer filters on, not the command id.
func TestSingleCommandReadIsAuditedAgainstItsNode(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorder, sink := audittest.New()
	enforcer := authz_service.NewEnforcer(
		batchMemberRepo{member: account_member.NewAccountMember(accountID, userID)},
		batchBindingRepo{bindings: []role_binding.RoleBinding{
			*role_binding.NewRoleBinding(accountID, userID, authz.RoleAdmin, nil, nil),
		}},
		batchRoleSettingRepo{}, batchOURepo{}, recorder, logger,
	)
	cmd := &nodecommand.NodeCommand{ID: uuid.New(), NodeID: uuid.New(), AccountID: accountID, Slug: "run_script"}
	h := &NodeHandler{
		nodeService:        &batchCountingNodeService{},
		nodeCommandService: singleCommandService{cmd: cmd},
		enforcer:           enforcer,
		audit:              recorder,
		logger:             logger,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/node-commands/"+cmd.ID.String(), nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(cmd.ID.String())
	c.Set("userID", userID.String())
	c.Set("accountID", accountID.String())

	if err := h.GetNodeCommand(c); err != nil {
		t.Fatalf("GetNodeCommand: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	event, ok := sink.Find(audit_event.ActionSecretValuesRead)
	if !ok {
		t.Fatalf("no secret-values read recorded; actions = %v", sink.Actions())
	}
	if event.TargetType != audit_event.TargetNode || event.TargetID != cmd.NodeID.String() {
		t.Fatalf("recorded target %s/%s, want node/%s", event.TargetType, event.TargetID, cmd.NodeID)
	}
}
