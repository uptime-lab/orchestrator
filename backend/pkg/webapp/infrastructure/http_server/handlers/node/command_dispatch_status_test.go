package node

// The generic command route answers what actually happened. Operator errors
// used to flatten into 500 "failed to send command" — an offline node, a
// malformed target device — and a command that was only queued for an offline
// node was answered "sent".

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
)

func postCommand(t *testing.T, h *NodeHandler, accountID, userID uuid.UUID, command string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"command": command, "params": map[string]any{}})
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

func TestAnOperatorErrorIsNotAnInternalError(t *testing.T) {
	nodeID := uuid.New()
	cases := []struct {
		name     string
		err      error
		status   int
		contains string
	}{
		{"offline node", &node_service.NodeUnavailableError{NodeID: nodeID, Slug: "reboot", State: "offline"}, http.StatusConflict, "node_unavailable"},
		{"unapproved node", &node_service.NodeUnavailableError{NodeID: nodeID, Slug: "reboot", State: "pending_approval", NotApproved: true}, http.StatusConflict, "node_not_approved"},
		{"already on the version", &node_service.AgentAlreadyCurrentError{NodeID: nodeID, Version: "1.2.3"}, http.StatusConflict, "agent_already_current"},
		{"stream gone", &node_service.NodeNotConnectedError{NodeID: nodeID}, http.StatusServiceUnavailable, "node is not connected"},
		{"refused parameter", app_errors.OperatorRefusal(errors.New("target_device must be a path under /dev")), http.StatusBadRequest, "target_device must be a path under /dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, accountID, userID := refusingHandler(t, tc.err)
			rec := postCommand(t, h, accountID, userID, "reboot")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.contains) {
				t.Errorf("body does not carry %q: %s", tc.contains, rec.Body.String())
			}
		})
	}
}

// The route's 409s keep its legacy bare body, which its clients parse:
// {"error": <code>, "message": …}, not the results envelope.
func TestAStateRefusalOfTheNodeOrAccountKeepsTheLegacyBody(t *testing.T) {
	nodeID := uuid.New()
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&node_service.RecoveryRequiredError{NodeID: nodeID, Slug: "capture_image"}, "recovery_required"},
		{&node_service.BootDriveEraseError{NodeID: nodeID, Device: "/dev/sda"}, "boot_drive"},
		{&node_service.CaptureImageLimitError{Err: errors.New("image limit reached for this account (max 5)")}, "image_limit"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			h, accountID, userID := refusingHandler(t, tc.err)
			rec := postCommand(t, h, accountID, userID, "reboot")
			if rec.Code != http.StatusConflict {
				t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the legacy {error,message} shape: %v: %s", err, rec.Body.String())
			}
			if body["error"] != tc.code || body["message"] != tc.err.Error() {
				t.Errorf("body = %v, want error=%q and the refusal as message", body, tc.code)
			}
		})
	}
}

// An error nobody marked as operator-facing still answers the generic 500
// without its text: arbitrary errors carry SQL and paths.
func TestAnUnmarkedErrorStaysGeneric(t *testing.T) {
	h, accountID, userID := refusingHandler(t, errors.New("pq: relation node_commands does not exist"))
	rec := postCommand(t, h, accountID, userID, "reboot")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") {
		t.Errorf("internal error text reached the caller: %s", rec.Body.String())
	}
}

// acceptingNodeService accepts the one-node batch, queued or not.
type acceptingNodeService struct {
	refusingNodeService
	queued    bool
	commandID uuid.UUID
}

func (f *acceptingNodeService) SendBatchCommand(_ context.Context, cmd node_command.BatchNodeCommandCommand) (*node_command.BatchCommandResult, error) {
	id := f.commandID
	return node_command.NewBatchCommandResult([]node_command.NodeCommandResultItem{{
		NodeID: cmd.NodeIDs[0], Success: true, CommandID: &id, Queued: f.queued,
	}}), nil
}

func TestTheSuccessAnswerSaysWhetherTheCommandWasQueued(t *testing.T) {
	for _, tc := range []struct {
		queued   bool
		delivery string
	}{{false, "dispatched"}, {true, "queued"}} {
		t.Run(tc.delivery, func(t *testing.T) {
			h, accountID, userID := refusingHandler(t, nil)
			svc := &acceptingNodeService{queued: tc.queued, commandID: uuid.New()}
			h.nodeService = svc
			rec := postCommand(t, h, accountID, userID, "set_hostname")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Data["delivery"] != tc.delivery {
				t.Errorf("delivery = %q, want %q", resp.Data["delivery"], tc.delivery)
			}
			if resp.Data["command_id"] != svc.commandID.String() {
				t.Errorf("command_id = %q, want %s", resp.Data["command_id"], svc.commandID)
			}
			if strings.Contains(rec.Body.String(), "sent successfully") {
				t.Errorf("the answer still claims the command was sent: %s", rec.Body.String())
			}
		})
	}
}
