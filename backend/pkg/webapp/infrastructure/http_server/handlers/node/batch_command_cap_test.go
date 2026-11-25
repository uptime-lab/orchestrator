package node

// The batch-command handler authorizes every supplied node id with its own
// database read, so the batch size limit has to bind BEFORE that loop. It used
// to bind only inside the service, i.e. after the loop had already run once per
// element of the request body.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodedomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
)

// batchCountingNodeService counts the per-id authorization reads and records
// what finally reached the service. The embedded nil interface panics on any
// other call, so the test also proves nothing else is touched.
type batchCountingNodeService struct {
	services.NodeService

	lookups    int
	dispatched []uuid.UUID
}

func (f *batchCountingNodeService) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*nodedomain.Node, error) {
	f.lookups++
	return &nodedomain.Node{ID: nodeID, AccountID: accountID}, nil
}

func (f *batchCountingNodeService) SendBatchCommand(_ context.Context, cmd node_command.BatchNodeCommandCommand) (*node_command.BatchCommandResult, error) {
	f.dispatched = append(f.dispatched, cmd.NodeIDs...)
	return node_command.NewBatchCommandResult(nil), nil
}

type batchMemberRepo struct {
	repository.AccountMemberRepository
	member *account_member.AccountMember
}

func (r batchMemberRepo) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	if r.member != nil && r.member.AccountID == accountID && r.member.UserID == userID {
		return r.member, nil
	}
	return nil, app_errors.ErrNotAMember
}

type batchBindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (r batchBindingRepo) ListByAccountAndUser(_ context.Context, _, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

type batchRoleSettingRepo struct {
	repository.AccountRoleSettingRepository
}

func (r batchRoleSettingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

type batchOURepo struct {
	repository.OrganizationalUnitRepository
}

func (r batchOURepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return nil, nil
}

// batchTestHandler wires the handler with a real enforcer over an account-wide
// admin binding, so authorization ALLOWS every target and the loop runs to
// completion — which is what makes the number of lookups meaningful.
func batchTestHandler(t *testing.T) (*NodeHandler, *batchCountingNodeService, uuid.UUID, uuid.UUID) {
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

	svc := &batchCountingNodeService{}
	return &NodeHandler{nodeService: svc, enforcer: enforcer, logger: logger}, svc, accountID, userID
}

func batchRequest(t *testing.T, accountID, userID uuid.UUID, nodeIDs []uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"command": "status_check", "node_ids": nodeIDs})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/commands", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("userID", userID.String())
	c.Set("accountID", accountID.String())
	return c, rec
}

func TestBatchCommandRefusesAnOversizedListBeforeAnyNodeLookup(t *testing.T) {
	h, svc, accountID, userID := batchTestHandler(t)

	ids := make([]uuid.UUID, 0, node_command.MaxBatchNodes+1)
	for i := 0; i <= node_command.MaxBatchNodes; i++ {
		ids = append(ids, uuid.New())
	}
	c, rec := batchRequest(t, accountID, userID, ids)

	if err := h.SendBatchCommand(c); err != nil {
		t.Fatalf("handler returned an error instead of a response: %v", err)
	}
	if svc.lookups != 0 {
		t.Errorf("an over-cap batch performed %d node lookups, want 0", svc.lookups)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if len(svc.dispatched) != 0 {
		t.Errorf("an over-cap batch reached the service with %d targets", len(svc.dispatched))
	}
}

// A repeated id is one node: it must cost one authorization read and produce one
// dispatch, not one of each per repetition.
func TestBatchCommandCollapsesRepeatedNodeIDs(t *testing.T) {
	h, svc, accountID, userID := batchTestHandler(t)

	nodeID := uuid.New()
	ids := make([]uuid.UUID, 0, node_command.MaxBatchNodes)
	for i := 0; i < node_command.MaxBatchNodes; i++ {
		ids = append(ids, nodeID)
	}
	c, rec := batchRequest(t, accountID, userID, ids)

	if err := h.SendBatchCommand(c); err != nil {
		t.Fatalf("handler returned an error instead of a response: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
	}
	if svc.lookups != 1 {
		t.Errorf("%d repeats of one id performed %d node lookups, want 1", node_command.MaxBatchNodes, svc.lookups)
	}
	if len(svc.dispatched) != 1 {
		t.Errorf("%d repeats of one id dispatched %d targets, want 1", node_command.MaxBatchNodes, len(svc.dispatched))
	}
}
