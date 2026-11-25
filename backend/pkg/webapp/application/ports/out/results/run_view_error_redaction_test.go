package results

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// A failing block echoes its arguments, so a block that resolved a secret into a
// param can carry it in its error text verbatim.
//
// The per-node command endpoint replaces those same bytes with a placeholder;
// the run view returned them at playbook.read, which RoleViewer holds.
func TestASecretFedBlocksErrorIsRedactedInTheRunView(t *testing.T) {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "secret-block", Type: "command.run_script", Params: map[string]interface{}{
				"script": "curl -H 'Authorization: {{secrets.api_token}}' https://example.invalid",
			}},
			{ID: "plain-block", Type: "command.reboot"},
		},
	}
	state := map[string]wen.GraphNodeState{
		"secret-block": {Status: wen.GraphNodeFailed, Error: "curl failed: Authorization: tok-abc123"},
		"plain-block":  {Status: wen.GraphNodeFailed, Error: "node offline"},
	}

	out := RedactGraphStateForGraph(state, graph)

	if got := out["secret-block"].Error; got == "curl failed: Authorization: tok-abc123" {
		t.Fatalf("the secret-fed block's error is verbatim: %q", got)
	}
	// And an ordinary block keeps its error, or a manager loses the only thing
	// they have to triage a run they are authorized to start.
	if got := out["plain-block"].Error; got != "node offline" {
		t.Fatalf("an ordinary block's error was redacted to %q; that is the triage text", got)
	}
}

// The node's ErrorMessage is a verbatim COPY of the failing block's message
// (engine.go's execNode.Fail), so redacting the graph state alone leaves the
// same bytes one field over.
func TestTheNodeErrorMessageDoesNotLeakTheSameText(t *testing.T) {
	const leaky = "curl failed: Authorization: tok-abc123"
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "secret-block", Type: "command.run_script", Params: map[string]interface{}{
				"script": "curl -H 'Authorization: {{secrets.api_token}}'",
			}},
		},
	}
	node := &wen.WorkflowExecutionNode{
		Status:       wen.StatusFailed,
		ErrorMessage: leaky,
		GraphState: map[string]wen.GraphNodeState{
			"secret-block": {Status: wen.GraphNodeFailed, Error: leaky},
		},
	}

	result := NewWorkflowExecutionNodeResultForRun(node, true, graph)
	if result.ErrorMessage == leaky {
		t.Fatalf("error_message still carries the block's secret-bearing text: %q", result.ErrorMessage)
	}
}

// Engine text must survive: it is what a manager triages with, and it is not a
// copy of any block's error.
func TestEngineTextSurvivesRedaction(t *testing.T) {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "secret-block", Type: "command.run_script", Params: map[string]interface{}{
				"script": "echo {{secrets.api_token}}",
			}},
		},
	}
	node := &wen.WorkflowExecutionNode{
		Status:       wen.StatusFailed,
		ErrorMessage: "the target node was deleted while this run was in flight",
		GraphState: map[string]wen.GraphNodeState{
			"secret-block": {Status: wen.GraphNodeFailed, Error: "curl failed: tok-abc"},
		},
	}

	result := NewWorkflowExecutionNodeResultForRun(node, true, graph)
	if result.ErrorMessage != "the target node was deleted while this run was in flight" {
		t.Fatalf("engine text was redacted to %q; a manager has nothing left to triage with", result.ErrorMessage)
	}
}
