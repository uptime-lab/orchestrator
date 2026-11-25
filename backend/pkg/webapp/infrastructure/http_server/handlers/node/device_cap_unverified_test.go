package node

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// unverifiedCapService refuses every admission because the device cap could
// not be checked, wrapped the way the service wraps it.
type unverifiedCapService struct {
	services.NodeService
}

func unverifiedRefusal() error {
	return &entitlement.DeviceCapUnverifiedError{Cap: 25,
		Cause: errors.New(`ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): SELECT pg_advisory_xact_lock`)}
}

func (unverifiedCapService) Create(context.Context, node_create.NodeCreateCommand) (*nodeDomain.Node, error) {
	return nil, unverifiedRefusal()
}

func (unverifiedCapService) ApproveNode(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*nodeDomain.Node, error) {
	return nil, unverifiedRefusal()
}

func (unverifiedCapService) RestoreDeleted(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*nodeDomain.Node, error) {
	return nil, unverifiedRefusal()
}

// GetByIDAndAccount lets the approve route's OU authorization find the node.
func (unverifiedCapService) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*nodeDomain.Node, error) {
	return &nodeDomain.Node{ID: nodeID, AccountID: accountID, ApprovalStatus: nodeDomain.ApprovalStatusPendingApproval}, nil
}

func (unverifiedCapService) ResolveSerialForBake(context.Context, uuid.UUID, string) (*nodeDomain.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}

// assertUnverifiedRefusal: a typed 503 with the stable code and the retry
// message, never the cap-reached 409 and never the database's own text.
func assertUnverifiedRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string         `json:"message"`
		Code    string         `json:"code"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != entitlement.DeviceCapUnverifiedErrorCode {
		t.Errorf("code = %q, want %q", body.Code, entitlement.DeviceCapUnverifiedErrorCode)
	}
	if !strings.Contains(body.Message, "could not be checked") || !strings.Contains(body.Message, "Try again") {
		t.Errorf("message %q does not say the limit could not be checked and to try again", body.Message)
	}
	if strings.Contains(rec.Body.String(), "SQLSTATE") || strings.Contains(rec.Body.String(), "pg_advisory") {
		t.Errorf("the database's error reached the client: %s", rec.Body.String())
	}
	if len(body.Data) != 0 {
		t.Errorf("data = %v, want none: nothing is known about the count", body.Data)
	}
}

func unverifiedContext(method, target, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", uuid.New().String())
	c.Set("userID", uuid.New().String())
	return c, rec
}

// Every HTTP path that admits a device answers a cap that could not be
// checked as the typed 503, so a client can tell "try again" from "remove a
// device".
func TestAdmittingRoutesAnswerAnUncheckableCapAs503(t *testing.T) {
	h := &NodeHandler{nodeService: unverifiedCapService{}, logger: slog.Default(), config: &config.Config{}}
	routes := []struct {
		name string
		run  func() (*httptest.ResponseRecorder, error)
	}{
		{"create", func() (*httptest.ResponseRecorder, error) {
			c, rec := unverifiedContext(http.MethodPost, "/api/v1/nodes", `{"name":"n"}`)
			return rec, h.Create(c)
		}},
		{"approve", func() (*httptest.ResponseRecorder, error) {
			// The approve route authorizes against the node's OU, so it runs
			// with a real enforcer over an account-wide admin binding.
			admin, _, accountID, userID := batchTestHandler(t)
			ah := &NodeHandler{nodeService: unverifiedCapService{}, enforcer: admin.enforcer, logger: slog.Default(), config: &config.Config{}}
			c, rec := unverifiedContext(http.MethodPost, "/", "")
			c.Set("accountID", accountID.String())
			c.Set("userID", userID.String())
			c.SetParamNames("id")
			c.SetParamValues(uuid.New().String())
			return rec, ah.Approve(c)
		}},
		{"restore", func() (*httptest.ResponseRecorder, error) {
			c, rec := unverifiedContext(http.MethodPost, "/", "")
			c.SetParamNames("id")
			c.SetParamValues(uuid.New().String())
			return rec, h.RestoreDeleted(c)
		}},
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			rec, err := r.run()
			if err != nil {
				t.Fatalf("handler returned a raw error: %v", err)
			}
			assertUnverifiedRefusal(t, rec)
		})
	}

	t.Run("webusb provision", func(t *testing.T) {
		wh, signed := webusbTestHandler(t, unverifiedCapService{})
		assertUnverifiedRefusal(t, webusbRequest(t, wh, uuid.New(), uuid.New(), "10000000abcdef12"))
		if len(*signed) != 0 {
			t.Errorf("the signer baked %d package(s) for a refused device", len(*signed))
		}
	})
}
