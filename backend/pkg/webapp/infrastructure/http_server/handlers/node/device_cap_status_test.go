package node

import (
	"context"
	"encoding/json"
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

// The device cap refusal is raised by the service's one row-creating step
// and must reach the client as the typed 409 on every HTTP path that admits
// a device, with the numbers in `data`. Each handler here answers through
// the shared SendInternalServerError, so the test is that nothing on the
// way flattens the type into a 500.

// capRefusingService refuses every Create at the cap and stubs what the
// WebUSB path touches before it.
type capRefusingService struct {
	services.NodeService
	created int
	// tenant: refuse as the service does for a caller who does not operate
	// the install.
	tenant bool
}

func (s *capRefusingService) Create(context.Context, node_create.NodeCreateCommand) (*nodeDomain.Node, error) {
	s.created++
	return nil, &entitlement.DeviceCapError{Cap: 25, Count: 25, SubscriptionURL: "https://example.com/plans",
		AccountDevices: 7, AccountDevicesKnown: true, InstanceOperator: !s.tenant}
}

func (s *capRefusingService) ResolveSerialForBake(context.Context, uuid.UUID, string) (*nodeDomain.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}

func assertDeviceCapRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
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
	if body.Code != entitlement.DeviceCapErrorCode {
		t.Errorf("code = %q, want %q", body.Code, entitlement.DeviceCapErrorCode)
	}
	if !strings.Contains(body.Message, "https://example.com/plans") {
		t.Errorf("message %q does not name the subscription URL", body.Message)
	}
	if body.Data["cap"] != float64(25) || body.Data["count"] != float64(25) || body.Data["subscription_url"] != "https://example.com/plans" ||
		body.Data["account_devices"] != float64(7) || body.Data["instance_operator"] != true {
		t.Errorf("data = %v, want the operator's full set", body.Data)
	}
}

// A tenant's 409 carries its own count, the cap and
// instance_operator:false, and nothing install-wide.
func TestCreateNodeAnswersATenantWithoutTheInstallCount(t *testing.T) {
	svc := &capRefusingService{tenant: true}
	h := &NodeHandler{nodeService: svc, logger: slog.Default(), config: &config.Config{}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(`{"name":"one-too-many"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", uuid.New().String())
	c.Set("userID", uuid.New().String())
	if err := h.Create(c); err != nil {
		t.Fatalf("handler returned a raw error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var body struct {
		Message string         `json:"message"`
		Code    string         `json:"code"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != entitlement.DeviceCapErrorCode || body.Message != entitlement.TenantRefusalMessage {
		t.Errorf("code %q message %q", body.Code, body.Message)
	}
	want := map[string]any{"account_devices": float64(7), "instance_operator": false, "cap": float64(25)}
	if len(body.Data) != len(want) {
		t.Fatalf("data = %v, want exactly %v", body.Data, want)
	}
	for k, v := range want {
		if body.Data[k] != v {
			t.Errorf("data[%q] = %v, want %v", k, body.Data[k], v)
		}
	}
}

func TestCreateNodeAnswersTheDeviceCapAs409(t *testing.T) {
	svc := &capRefusingService{}
	h := &NodeHandler{nodeService: svc, logger: slog.Default(), config: &config.Config{}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(`{"name":"one-too-many"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", uuid.New().String())
	c.Set("userID", uuid.New().String())
	if err := h.Create(c); err != nil {
		t.Fatalf("handler returned a raw error: %v", err)
	}
	assertDeviceCapRefusal(t, rec)
}

func TestWebUSBProvisionAnswersTheDeviceCapAs409(t *testing.T) {
	svc := &capRefusingService{}
	h, signed := webusbTestHandler(t, svc)

	rec := webusbRequest(t, h, uuid.New(), uuid.New(), "10000000abcdef12")

	assertDeviceCapRefusal(t, rec)
	if len(*signed) != 0 {
		t.Errorf("the signer baked %d package(s) for a refused device", len(*signed))
	}
}
