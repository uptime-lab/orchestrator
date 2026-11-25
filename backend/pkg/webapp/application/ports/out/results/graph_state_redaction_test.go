package results

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// A condition comparing {{secrets.api_key}} to a literal answers "did it match?"
// through the port it routed to. Redacting outputs["result"] and returning
// chosen_port is the same fact leaking around the redaction, one comparison at a
// time.
func TestRedactGraphStateClearsTheChosenPortOracle(t *testing.T) {
	state := map[string]wen.GraphNodeState{
		"cond": {
			Status:     wen.GraphNodeCompleted,
			ChosenPort: "true",
			Outputs:    map[string]string{"result": "true"},
		},
		"read": {
			Status:  wen.GraphNodeCompleted,
			Outputs: map[string]string{"content": "hunter2"},
			Error:   "",
		},
		"boom": {
			Status: wen.GraphNodeFailed,
			Error:  "node offline",
		},
	}

	out := RedactGraphState(state)

	if out["cond"].ChosenPort != "" {
		t.Errorf("chosen_port = %q, want it cleared", out["cond"].ChosenPort)
	}
	if out["cond"].Outputs["result"] != redactedPlaceholder {
		t.Errorf("condition output = %q, want the placeholder", out["cond"].Outputs["result"])
	}
	if out["read"].Outputs["content"] != redactedPlaceholder {
		t.Errorf("block output = %q, want the placeholder", out["read"].Outputs["content"])
	}
	// Error text stays readable on purpose — a manager may run a playbook
	// without holding secret.read_value and still has to triage its failures.
	if out["boom"].Error != "node offline" {
		t.Errorf("error text = %q, want it left intact", out["boom"].Error)
	}
	// The caller's own map must not be rewritten.
	if state["cond"].ChosenPort != "true" {
		t.Error("RedactGraphState must not mutate its input")
	}
}

// Redacting outputs and chosen_port is not enough: `completed` on one branch
// and `skipped` on the other IS the condition's answer. A manager who may run
// a playbook but not read secret values could put {{secrets.api_key}} on one
// side of a `contains` and read the value out a character at a time, with no
// device touched and no privileged block involved.
func TestRedactionHidesWhichBranchASecretConditionTook(t *testing.T) {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "cond", Type: workflow_block.TypeCondition, Params: map[string]interface{}{
				"left": "{{secrets.api_key}}", "op": "contains", "right": "a",
			}},
			{ID: "yes", Type: workflow_block.TypeWait},
			{ID: "no", Type: workflow_block.TypeWait},
		},
		Edges: []workflow_template.GraphEdge{
			{ID: "e0", From: "start", FromPort: "out", To: "cond"},
			{ID: "e1", From: "cond", FromPort: workflow_block.PortTrue, To: "yes"},
			{ID: "e2", From: "cond", FromPort: workflow_block.PortFalse, To: "no"},
		},
	}
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"cond":  {Status: wen.GraphNodeCompleted, ChosenPort: workflow_block.PortTrue},
		"yes":   {Status: wen.GraphNodeCompleted},
		"no":    {Status: wen.GraphNodeSkipped},
	}

	out := RedactGraphStateForGraph(state, graph)

	for _, id := range []string{"cond", "yes", "no"} {
		if _, present := out[id]; present {
			t.Errorf("block %q is still in the response; its status answers the comparison", id)
		}
	}
	if _, present := out["start"]; !present {
		t.Error("blocks upstream of the condition must stay visible")
	}
	if len(state) != 4 {
		t.Error("RedactGraphStateForGraph must not mutate its input")
	}
}

// An ordinary run must stay fully readable — the whole value of the run panel
// is the per-block progress.
func TestRedactionKeepsEveryBlockOfAnOrdinaryRun(t *testing.T) {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "cond", Type: workflow_block.TypeCondition, Params: map[string]interface{}{
				"left": "{{node.hostname}}", "op": "eq", "right": "lab-01",
			}},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e0", From: "start", FromPort: "out", To: "cond"}},
	}
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"cond":  {Status: wen.GraphNodeCompleted, ChosenPort: workflow_block.PortTrue},
	}

	out := RedactGraphStateForGraph(state, graph)

	if len(out) != 2 {
		t.Errorf("graph state = %d blocks, want 2", len(out))
	}
	if out["cond"].ChosenPort != "" {
		t.Error("chosen_port is still the condition's answer and stays cleared")
	}
}

// A run pinned before snapshotting existed carries no graph; redaction must
// degrade to what it always did rather than hiding everything or nothing.
func TestRedactionWithNoGraphStillStripsFields(t *testing.T) {
	state := map[string]wen.GraphNodeState{
		"b1": {
			Status:     wen.GraphNodeCompleted,
			ChosenPort: workflow_block.PortTrue,
			Outputs:    map[string]string{"stdout": "hunter2"},
		},
	}

	out := RedactGraphStateForGraph(state, nil)

	if len(out) != 1 {
		t.Fatalf("graph state = %d blocks, want 1", len(out))
	}
	if out["b1"].Outputs["stdout"] == "hunter2" || out["b1"].ChosenPort != "" {
		t.Errorf("field-level redaction did not run: %+v", out["b1"])
	}
}
