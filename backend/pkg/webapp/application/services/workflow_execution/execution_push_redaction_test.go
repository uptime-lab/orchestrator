package workflow_execution

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type capturingExecutionSocket struct {
	sent []websockets.Message
}

func (c *capturingExecutionSocket) Send(m websockets.Message) error {
	c.sent = append(c.sent, m)
	return nil
}

func (c *capturingExecutionSocket) GetChannel() chan websockets.Message { return nil }

const pushCanary = "push-canary-literal-credential"

// The execution frame is a second read path to the run: it may carry the
// pinned authored document only to recipients who may read secret values (the
// REST read redacts it for everyone else), and reaches only holders of
// playbook.read, the action every execution read route demands.
func TestExecutionFrameFollowsTheRestRead(t *testing.T) {
	sock := &capturingExecutionSocket{}
	s := &Service{websocketClient: sock, logger: slog.Default()}
	ou := uuid.New()
	exec := &workflow_execution.WorkflowExecution{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		OUID:      &ou,
		TemplateSnapshot: &workflow_template.TemplateSnapshot{
			Graph: &workflow_template.WorkflowGraph{
				SchemaVersion: workflow_template.GraphSchemaVersion,
				Nodes: []workflow_template.GraphNode{{
					ID: "s1", Type: "agent.run_script",
					Params: map[string]interface{}{"script": "curl -H 'Authorization: Bearer " + pushCanary + "' https://x"},
				}},
			},
		},
	}

	s.notifyExecutionUpdated(exec)

	if len(sock.sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(sock.sent))
	}
	msg := sock.sent[0]
	if msg.ScopeOU == nil || *msg.ScopeOU != ou {
		t.Fatalf("the frame must stay scoped to the run's unit, got %v", msg.ScopeOU)
	}
	if msg.RequiredAction != authz.ActionPlaybookRead {
		t.Fatalf("RequiredAction = %q, want %q", msg.RequiredAction, authz.ActionPlaybookRead)
	}
	if msg.FullPayloadAction != authz.ActionSecretReadValue {
		t.Fatalf("FullPayloadAction = %q, want %q", msg.FullPayloadAction, authz.ActionSecretReadValue)
	}
	if msg.RedactedPayload == nil {
		t.Fatal("recipients without secret.read_value must get a redacted variant, not the full frame")
	}
	if got := string(msg.RedactedPayload.ToBytes()); strings.Contains(got, pushCanary) {
		t.Fatalf("the redacted variant carries the literal credential: %s", got)
	}
	if got := string(msg.Payload.ToBytes()); !strings.Contains(got, pushCanary) {
		t.Fatalf("the full variant must keep the snapshot for privileged recipients: %s", got)
	}
	// Redacting the variant must not have touched the caller's execution.
	if p := exec.TemplateSnapshot.Graph.Nodes[0].Params["script"].(string); !strings.Contains(p, pushCanary) {
		t.Fatal("redacting the push mutated the stored snapshot")
	}
}
