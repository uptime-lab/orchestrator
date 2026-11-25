package workflow_execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// stubSecretWriter satisfies the engine's secretWriter interface, recording the
// attribute call so the test can assert what would reach the secret service.
type stubSecretWriter struct {
	attrCalls int
	actor     audit.Actor
	nodeID    uuid.UUID
	key       string
	value     string
	sensitive bool
	err       error
	// The secret_upsert call, recorded the same way.
	upsertCalls int
	upsertActor audit.Actor
	upsertOpts  secret_domain.UpsertOptions
}

func (s *stubSecretWriter) UpsertForNode(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, name string, kind secret_domain.SecretKind, key, value string, opts secret_domain.UpsertOptions) error {
	s.upsertCalls++
	s.upsertActor = actor
	s.upsertOpts = opts
	return s.err
}

func (s *stubSecretWriter) UpsertNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key, value string, sensitive bool) (*secret_domain.NodeAttribute, error) {
	s.attrCalls++
	s.actor = actor
	s.nodeID = nodeID
	s.key = key
	s.value = value
	s.sensitive = sensitive
	if s.err != nil {
		return nil, s.err
	}
	return &secret_domain.NodeAttribute{Key: key, Value: value, Sensitive: sensitive}, nil
}

func attributeUpsertRun(writer *stubSecretWriter, params map[string]interface{}, rc resolveContext) (*engineRun, workflow_template.GraphNode) {
	node := workflow_template.GraphNode{ID: "b", Type: workflow_block.TypeAttributeUpsert, Params: params}
	svc := &Service{logger: slog.Default()}
	if writer != nil {
		svc.secretWriter = writer
	}
	r := &engineRun{
		svc: svc,
		ctx: context.Background(),
		rc:  rc,
		execNode: &wen.WorkflowExecutionNode{
			ID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(),
			GraphState: map[string]wen.GraphNodeState{},
		},
		graph: &workflow_template.WorkflowGraph{Nodes: []workflow_template.GraphNode{node}},
	}
	return r, node
}

func TestExecAttributeUpsert(t *testing.T) {
	t.Run("resolves refs and writes through the attribute path", func(t *testing.T) {
		writer := &stubSecretWriter{}
		rc := resolveContext{outputs: map[string]wen.GraphNodeState{
			"wait": {Outputs: map[string]string{"match": "tok-xyz"}},
		}}
		r, node := attributeUpsertRun(writer, map[string]interface{}{
			"key":       "  ci_agent_token  ",
			"value":     "{{blocks.wait.outputs.match}}",
			"sensitive": true,
		}, rc)
		r.execAttributeUpsert("b", node)

		if writer.attrCalls != 1 {
			t.Fatalf("expected 1 UpsertNodeAttribute call, got %d", writer.attrCalls)
		}
		if writer.key != "ci_agent_token" {
			t.Errorf("key = %q, want trimmed 'ci_agent_token'", writer.key)
		}
		if writer.value != "tok-xyz" {
			t.Errorf("value = %q, want resolved 'tok-xyz'", writer.value)
		}
		if !writer.sensitive {
			t.Error("sensitive=true was not passed through")
		}
		if writer.nodeID != r.execNode.NodeID {
			t.Error("the attribute must target the execution node, nothing else")
		}
		// A run with no execution row in hand (none set here) and no author has
		// no member to name, so the write is the platform's.
		if writer.actor.UserID != uuid.Nil || writer.actor.AccountID != r.execNode.AccountID {
			t.Errorf("expected the system actor for this account, got %+v", writer.actor)
		}
		st := r.execNode.GraphState["b"]
		if st.Status != wen.GraphNodeCompleted {
			t.Fatalf("expected completed, got %s (%s)", st.Status, st.Error)
		}
		if st.Outputs["key"] != "ci_agent_token" {
			t.Errorf("expected key output, got %q", st.Outputs["key"])
		}
	})

	t.Run("fails on an empty key without calling the writer", func(t *testing.T) {
		writer := &stubSecretWriter{}
		r, node := attributeUpsertRun(writer, map[string]interface{}{
			"key": "", "value": "v",
		}, resolveContext{})
		r.execAttributeUpsert("b", node)

		if writer.attrCalls != 0 {
			t.Fatalf("expected no writer call, got %d", writer.attrCalls)
		}
		if st := r.execNode.GraphState["b"]; st.Status != wen.GraphNodeFailed {
			t.Fatalf("expected failed, got %s", st.Status)
		}
	})

	t.Run("a writer error fails the block with the service's message", func(t *testing.T) {
		writer := &stubSecretWriter{err: errors.New("secret quota exceeded")}
		r, node := attributeUpsertRun(writer, map[string]interface{}{
			"key": "rack", "value": "A3",
		}, resolveContext{})
		r.execAttributeUpsert("b", node)

		st := r.execNode.GraphState["b"]
		if st.Status != wen.GraphNodeFailed {
			t.Fatalf("expected failed, got %s", st.Status)
		}
	})

	t.Run("fails without a wired secret writer", func(t *testing.T) {
		r, node := attributeUpsertRun(nil, map[string]interface{}{
			"key": "rack", "value": "A3",
		}, resolveContext{})
		r.execAttributeUpsert("b", node)

		if st := r.execNode.GraphState["b"]; st.Status != wen.GraphNodeFailed {
			t.Fatalf("expected failed, got %s", st.Status)
		}
	})
}
