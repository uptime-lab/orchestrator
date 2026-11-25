package node

// A set_hostname value the device would reject is refused before dispatch. The
// refusal has to reach the operator as a 400 naming the rule: the generic
// fallback below it answers 500 "failed to send command", which reads as the
// platform breaking rather than as the value being wrong. The oversized-payload
// refusal has the same shape, so this test covers both shapes of the arm.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodedomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	nodeCommandDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// refusingNodeService fails the dispatch the way the service does for a hostname
// the device would reject — wrapped, because the handler matches with errors.Is
// and a %v would silently defeat it.
type refusingNodeService struct {
	services.NodeService
	err error
}

func (f *refusingNodeService) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*nodedomain.Node, error) {
	return &nodedomain.Node{ID: nodeID, AccountID: accountID}, nil
}

func (f *refusingNodeService) SendCommand(_ context.Context, _ node_command.NodeCommandCommand) error {
	return f.err
}

// SendBatchCommand is the route the handler dispatches through; it fails the
// one item the way the service does, typed error preserved in Err.
func (f *refusingNodeService) SendBatchCommand(_ context.Context, cmd node_command.BatchNodeCommandCommand) (*node_command.BatchCommandResult, error) {
	items := make([]node_command.NodeCommandResultItem, 0, len(cmd.NodeIDs))
	for _, id := range cmd.NodeIDs {
		items = append(items, node_command.NodeCommandResultItem{NodeID: id, Error: f.err.Error(), Err: f.err})
	}
	return node_command.NewBatchCommandResult(items), nil
}

func refusingHandler(t *testing.T, err error) (*NodeHandler, uuid.UUID, uuid.UUID) {
	t.Helper()

	accountID := uuid.New()
	userID := uuid.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditRecorder, _ := audittest.New()

	enforcer := authz_service.NewEnforcer(
		batchMemberRepo{member: account_member.NewAccountMember(accountID, userID)},
		batchBindingRepo{bindings: []role_binding.RoleBinding{
			*role_binding.NewRoleBinding(accountID, userID, authz.RoleAdmin, nil, nil),
		}},
		batchRoleSettingRepo{},
		batchOURepo{},
		auditRecorder,
		logger,
	)

	return &NodeHandler{nodeService: &refusingNodeService{err: err}, enforcer: enforcer, logger: logger}, accountID, userID
}

func sendHostnameCommand(t *testing.T, h *NodeHandler, accountID, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"command": "set_hostname",
		"params":  map[string]any{"hostname": "not a valid hostname"},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	nodeID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID.String()+"/command", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(nodeID.String())
	c.Set("userID", userID.String())
	c.Set("accountID", accountID.String())

	if err := h.SendCommand(c); err != nil {
		t.Fatalf("handler returned an error instead of a response: %v", err)
	}
	return rec
}

func TestARejectedHostnameAnswers400AndNamesTheRule(t *testing.T) {
	h, accountID, userID := refusingHandler(t, fmt.Errorf("set_hostname: %w", nodeCommandDomain.ErrInvalidHostname))

	rec := sendHostnameCommand(t, h, accountID, userID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want %d — the refusal flattened into the generic dispatch failure", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "RFC 1123") {
		t.Errorf("the response does not state the rule the operator broke: %s", rec.Body.String())
	}
}

// The refusal must never carry the value back: a dispatch error is written
// verbatim into node_commands.details, which is plaintext and readable by every
// node.read caller, and the value can be a resolved {{secrets.*}}.
func TestARejectedHostnameIsNotEchoedToTheCaller(t *testing.T) {
	h, accountID, userID := refusingHandler(t, fmt.Errorf("set_hostname: %w", nodeCommandDomain.ErrInvalidHostname))

	rec := sendHostnameCommand(t, h, accountID, userID)

	if strings.Contains(rec.Body.String(), "not a valid hostname") {
		t.Errorf("the refusal quoted the rejected value back: %s", rec.Body.String())
	}
}
