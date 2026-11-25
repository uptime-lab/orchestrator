package results

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

func authoringTestGraph() *workflow_template.WorkflowGraph {
	return &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: "trigger.schedule", Params: map[string]interface{}{
				// cron is a validated type: it must survive, or the viewer
				// loses the one fact the schedule column is built from.
				"cron": "0 3 * * *",
			}},
			{ID: "s1", Type: "agent.run_script", Params: map[string]interface{}{
				// Free text: the exact place a literal credential lives.
				"script":          "curl -H 'Authorization: Bearer hunter2' https://x",
				"timeout_seconds": float64(30),
			}},
			{ID: "s2", Type: "not.a.block", Params: map[string]interface{}{
				// Unknown kind fails CLOSED: every string goes, wherever it
				// hides.
				"anything": "literal-password",
				"nested":   map[string]interface{}{"deep": "another-secret"},
				"listed":   []interface{}{"one", float64(2)},
			}},
		},
	}
}

// Free-text params redact, validated types survive, unknown kinds fail
// closed, and numbers pass through — they cannot carry a credential.
func TestRedactAuthoringFollowsParamTypes(t *testing.T) {
	result := &WorkflowTemplateResult{Graph: authoringTestGraph()}
	result.RedactAuthoring()

	nodes := map[string]workflow_template.GraphNode{}
	for _, n := range result.Graph.Nodes {
		nodes[n.ID] = n
	}
	if got := nodes["start"].Params["cron"]; got != "0 3 * * *" {
		t.Fatalf("cron is save-time validated and must survive, got %v", got)
	}
	if got := nodes["s1"].Params["script"]; got != redactedPlaceholder {
		t.Fatalf("script must redact, got %v", got)
	}
	if got := nodes["s1"].Params["timeout_seconds"]; got != float64(30) {
		t.Fatalf("a number must survive, got %v", got)
	}
	if got := nodes["s2"].Params["anything"]; got != redactedPlaceholder {
		t.Fatalf("unknown block kind must fail closed, got %v", got)
	}
	nested := nodes["s2"].Params["nested"].(map[string]interface{})
	if nested["deep"] != redactedPlaceholder {
		t.Fatalf("a nested string must redact, got %v", nested["deep"])
	}
	listed := nodes["s2"].Params["listed"].([]interface{})
	if listed[0] != redactedPlaceholder || listed[1] != float64(2) {
		t.Fatalf("list redaction wrong: %v", listed)
	}
}

// The redaction must not mutate the graph it was given: the same domain
// object backs the unredacted response for the next (privileged) caller.
func TestRedactAuthoringDoesNotMutateTheOriginal(t *testing.T) {
	graph := authoringTestGraph()
	result := &WorkflowTemplateResult{Graph: graph}
	result.RedactAuthoring()

	if graph.Nodes[1].Params["script"] == redactedPlaceholder {
		t.Fatal("the original graph was mutated")
	}
}

// The execution snapshot redacts the same way — it is the same authored
// document, pinned at start.
func TestRedactAuthoringCoversTheSnapshot(t *testing.T) {
	result := &WorkflowExecutionResult{TemplateSnapshot: &workflow_template.TemplateSnapshot{
		Graph: authoringTestGraph(),
	}}
	result.RedactAuthoring()
	if got := result.TemplateSnapshot.Graph.Nodes[1].Params["script"]; got != redactedPlaceholder {
		t.Fatalf("snapshot script must redact, got %v", got)
	}
}

// The save path refuses the placeholder, which is the other half of the
// design: a redacted read cannot round-trip into a silent overwrite.
func TestValidateRefusesTheRedactionPlaceholder(t *testing.T) {
	graph := authoringTestGraph()
	graph.Nodes[1].Params["script"] = redactedPlaceholder
	template := &workflow_template.WorkflowTemplate{
		Name:   "t",
		Status: workflow_template.StatusActive,
		Graph:  graph,
	}
	err := template.Validate()
	if err == nil {
		t.Fatal("a document carrying the placeholder must refuse to save")
	}
	if !strings.Contains(err.Error(), "secret.read_value") {
		t.Fatalf("the refusal must name the missing permission, got %q", err.Error())
	}
}
