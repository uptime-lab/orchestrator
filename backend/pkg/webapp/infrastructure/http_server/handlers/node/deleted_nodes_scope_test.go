package node

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// deletedScopeService records the account each deleted-node route hands the
// service, and answers as a service that holds no record for it.
type deletedScopeService struct {
	services.NodeService
	accounts   []uuid.UUID
	restoreErr error
}

func (s *deletedScopeService) ListDeleted(_ context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error) {
	s.accounts = append(s.accounts, accountID)
	return nil, nil
}

func (s *deletedScopeService) RestoreDeleted(_ context.Context, accountID, _, _ uuid.UUID) (*nodeDomain.Node, error) {
	s.accounts = append(s.accounts, accountID)
	if s.restoreErr != nil {
		return nil, s.restoreErr
	}
	return nil, app_errors.ErrNotFound
}

func (s *deletedScopeService) ForgetDeleted(_ context.Context, accountID, _, _ uuid.UUID) error {
	s.accounts = append(s.accounts, accountID)
	return app_errors.ErrNotFound
}

// The deleted-node routes take the account from the authenticated context and
// nowhere else: the path carries only the node id, so a caller of another
// account naming a record's id reaches the service under ITS OWN account and is
// answered 404.
func TestDeletedNodeRoutesScopeByTheCallersAccount(t *testing.T) {
	svc := &deletedScopeService{}
	h := &NodeHandler{nodeService: svc, logger: slog.Default(), config: &config.Config{}}
	recordID := uuid.New().String()

	c, rec := unverifiedContext(http.MethodGet, "/", "")
	caller := c.Get("accountID").(string)
	if err := h.ListDeleted(c); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: err %v, status %d", err, rec.Code)
	}
	for _, run := range []func() (int, string){
		func() (int, string) {
			c, rec := unverifiedContext(http.MethodPost, "/", "")
			c.Set("accountID", caller)
			c.SetParamNames("id")
			c.SetParamValues(recordID)
			_ = h.RestoreDeleted(c)
			return rec.Code, "restore"
		},
		func() (int, string) {
			c, rec := unverifiedContext(http.MethodDelete, "/", "")
			c.Set("accountID", caller)
			c.SetParamNames("id")
			c.SetParamValues(recordID)
			_ = h.ForgetDeleted(c)
			return rec.Code, "forget"
		},
	} {
		if code, name := run(); code != http.StatusNotFound {
			t.Errorf("%s of another account's record answered %d, want 404", name, code)
		}
	}
	if len(svc.accounts) != 3 {
		t.Fatalf("service reached %d times, want 3", len(svc.accounts))
	}
	for i, got := range svc.accounts {
		if got.String() != caller {
			t.Errorf("call %d ran under account %s, want the caller's %s", i, got, caller)
		}
	}
}

// The same-account refusal travels as its typed 409 with a stable code and the
// live node in data, never as a 500.
func TestRestoreRefusedForASerialHeldInTheAccountIs409(t *testing.T) {
	holder := uuid.New()
	svc := &deletedScopeService{restoreErr: &nodesvc.SerialBoundInAccountError{
		NodeID: uuid.New(), Serial: "10000000abcd0001", HolderID: holder, HolderName: "bench-pi",
	}}
	h := &NodeHandler{nodeService: svc, logger: slog.Default(), config: &config.Config{}}
	c, rec := unverifiedContext(http.MethodPost, "/", "")
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())
	if err := h.RestoreDeleted(c); err != nil {
		t.Fatalf("handler returned a raw error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string         `json:"message"`
		Code    string         `json:"code"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "serial_bound_in_account" {
		t.Errorf("code = %q, want serial_bound_in_account", body.Code)
	}
	if body.Data["node_id"] != holder.String() || body.Data["node_name"] != "bench-pi" {
		t.Errorf("data = %v, want the live node", body.Data)
	}
}
