package workflow_execution

import (
	"context"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/google/uuid"
)

type stubPolicyWriter struct {
	calls        int
	networkFirst *organizational_unit.PolicyFlag
	bootToOS     *organizational_unit.PolicyFlag
	err          error
}

func (s *stubPolicyWriter) SetNodePolicy(ctx context.Context, _ audit.Actor, accountID, nodeID uuid.UUID, networkFirst, bootToOS, sshPasswordAuth *organizational_unit.PolicyFlag) error {
	s.calls++
	s.networkFirst = networkFirst
	s.bootToOS = bootToOS
	return s.err
}

func setBootPolicyRun(writer *stubPolicyWriter, params map[string]interface{}) (*engineRun, workflow_template.GraphNode) {
	node := workflow_template.GraphNode{ID: "b", Type: workflow_block.TypeSetBootPolicy, Params: params}
	svc := &Service{logger: slog.Default()}
	if writer != nil {
		svc.nodePolicyWriter = writer
	}
	r := &engineRun{
		svc: svc,
		ctx: context.Background(),
		execNode: &wen.WorkflowExecutionNode{
			ID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(),
			GraphState: map[string]wen.GraphNodeState{},
		},
		graph: &workflow_template.WorkflowGraph{Nodes: []workflow_template.GraphNode{node}},
	}
	return r, node
}

func TestExecSetBootPolicy(t *testing.T) {
	t.Run("applies selected flags and completes", func(t *testing.T) {
		writer := &stubPolicyWriter{}
		r, node := setBootPolicyRun(writer, map[string]interface{}{
			"boot_to_os": "on", "network_first": "unchanged",
		})
		r.execSetBootPolicy("b", node)

		if writer.calls != 1 {
			t.Fatalf("expected 1 SetNodePolicy call, got %d", writer.calls)
		}
		if writer.bootToOS == nil || *writer.bootToOS != organizational_unit.PolicyOn {
			t.Fatalf("expected boot_to_os=on, got %v", writer.bootToOS)
		}
		if writer.networkFirst != nil {
			t.Fatalf("expected network_first unchanged (nil), got %v", *writer.networkFirst)
		}
		st := r.execNode.GraphState["b"]
		if st.Status != wen.GraphNodeCompleted {
			t.Fatalf("expected completed, got %s (%s)", st.Status, st.Error)
		}
		if st.Outputs["boot_to_os"] != "on" {
			t.Fatalf("expected boot_to_os output 'on', got %q", st.Outputs["boot_to_os"])
		}
		if _, present := st.Outputs["network_first"]; present {
			t.Fatal("unchanged network_first must not appear in outputs")
		}
	})

	t.Run("fails when nothing would change", func(t *testing.T) {
		writer := &stubPolicyWriter{}
		r, node := setBootPolicyRun(writer, map[string]interface{}{
			"boot_to_os": "unchanged", "network_first": "unchanged",
		})
		r.execSetBootPolicy("b", node)

		if writer.calls != 0 {
			t.Fatalf("expected no SetNodePolicy call, got %d", writer.calls)
		}
		if st := r.execNode.GraphState["b"]; st.Status != wen.GraphNodeFailed {
			t.Fatalf("expected failed, got %s", st.Status)
		}
	})

	t.Run("fails without a wired policy writer", func(t *testing.T) {
		r, node := setBootPolicyRun(nil, map[string]interface{}{"boot_to_os": "on"})
		r.execSetBootPolicy("b", node)

		if st := r.execNode.GraphState["b"]; st.Status != wen.GraphNodeFailed {
			t.Fatalf("expected failed, got %s", st.Status)
		}
	})
}

func TestPolicyFlagParam(t *testing.T) {
	cases := []struct {
		in   interface{}
		want *organizational_unit.PolicyFlag
	}{
		{"on", ptrFlag(organizational_unit.PolicyOn)},
		{"off", ptrFlag(organizational_unit.PolicyOff)},
		{"inherit", ptrFlag(organizational_unit.PolicyInherit)},
		{"unchanged", nil},
		{"", nil},
		{nil, nil},
		{42, nil},
	}
	for _, tc := range cases {
		got := policyFlagParam(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("policyFlagParam(%v) = %v, want nil", tc.in, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("policyFlagParam(%v) = %v, want %v", tc.in, got, *tc.want)
		}
	}
}

func ptrFlag(f organizational_unit.PolicyFlag) *organizational_unit.PolicyFlag { return &f }
