package workflow_template

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

func chainGraph() *WorkflowGraph {
	return &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "a", Type: "command.reboot"},
			{ID: "b", Type: "command.status_check"},
		},
		Edges: []GraphEdge{
			{From: "start", To: "a"},
			{From: "a", To: "b"},
		},
	}
}

func TestGraphValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(g *WorkflowGraph)
		wantErr string
	}{
		{"valid chain", func(g *WorkflowGraph) {}, ""},
		{"wrong schema version", func(g *WorkflowGraph) { g.SchemaVersion = 2 }, "schema_version"},
		{"no nodes", func(g *WorkflowGraph) { g.Nodes = nil; g.Edges = nil }, "at least one node"},
		{"missing trigger", func(g *WorkflowGraph) { g.Nodes = g.Nodes[1:]; g.Edges = g.Edges[1:] }, "exactly one trigger block"},
		{"two triggers", func(g *WorkflowGraph) {
			g.Nodes = append(g.Nodes, GraphNode{ID: "start2", Type: workflow_block.TypeStart})
		}, "exactly one trigger block"},
		{"schedule trigger ok", func(g *WorkflowGraph) {
			g.Nodes[0] = GraphNode{ID: "start", Type: workflow_block.TypeSchedule, Params: map[string]interface{}{"cron": "0 3 * * 1"}}
		}, ""},
		{"duplicate node id", func(g *WorkflowGraph) {
			g.Nodes = append(g.Nodes, GraphNode{ID: "a", Type: "command.reboot"})
		}, "duplicate node id"},
		{"unknown block type", func(g *WorkflowGraph) { g.Nodes[1].Type = "command.nope" }, "unknown block type"},
		{"invalid params", func(g *WorkflowGraph) {
			g.Nodes[1].Type = "command.erase_drive"
		}, "'target_device' is required"},
		{"unknown edge target", func(g *WorkflowGraph) { g.Edges[1].To = "ghost" }, "unknown target node"},
		{"self loop", func(g *WorkflowGraph) { g.Edges[1].To = "a" }, "self-loop"},
		{"bad output port", func(g *WorkflowGraph) { g.Edges[0].FromPort = "true" }, "no output port"},
		{"parallel fan-out on default port ok", func(g *WorkflowGraph) {
			g.Nodes = append(g.Nodes, GraphNode{ID: "c", Type: "command.reboot"})
			g.Edges = append(g.Edges, GraphEdge{From: "a", To: "c"})
		}, ""},
		{"branching incoming without join", func(g *WorkflowGraph) {
			g.Nodes = append(g.Nodes, GraphNode{ID: "c", Type: "command.reboot"})
			g.Edges = append(g.Edges, GraphEdge{From: "c", To: "b"})
		}, "use a Join block"},
		{"join accepts multiple incoming", func(g *WorkflowGraph) {
			// start -> a, start -> c, a -> join, c -> join, join -> b
			g.Nodes = append(g.Nodes,
				GraphNode{ID: "c", Type: "command.status_check"},
				GraphNode{ID: "j", Type: workflow_block.TypeJoin})
			g.Edges = []GraphEdge{
				{From: "start", To: "a"}, {From: "start", To: "c"},
				{From: "a", To: "j"}, {From: "c", To: "j"}, {From: "j", To: "b"},
			}
		}, ""},
		{"condition branches ok", func(g *WorkflowGraph) {
			g.Nodes = []GraphNode{
				{ID: "start", Type: workflow_block.TypeStart},
				{ID: "cond", Type: workflow_block.TypeCondition, Params: map[string]interface{}{"left": "{{node.model}}", "op": "contains", "right": "CM5"}},
				{ID: "a", Type: "command.reboot"},
				{ID: "b", Type: "command.status_check"},
			}
			g.Edges = []GraphEdge{
				{From: "start", To: "cond"},
				{From: "cond", To: "a", FromPort: workflow_block.PortTrue},
				{From: "cond", To: "b", FromPort: workflow_block.PortFalse},
			}
		}, ""},
		{"condition port double edge rejected", func(g *WorkflowGraph) {
			g.Nodes = []GraphNode{
				{ID: "start", Type: workflow_block.TypeStart},
				{ID: "cond", Type: workflow_block.TypeCondition, Params: map[string]interface{}{"left": "x", "op": "eq", "right": "y"}},
				{ID: "a", Type: "command.reboot"},
				{ID: "b", Type: "command.status_check"},
			}
			g.Edges = []GraphEdge{
				{From: "start", To: "cond"},
				{From: "cond", To: "a", FromPort: workflow_block.PortTrue},
				{From: "cond", To: "b", FromPort: workflow_block.PortTrue},
			}
		}, "more than one outgoing edge"},
		{"bad policy", func(g *WorkflowGraph) {
			g.Nodes[1].Policy = &BlockPolicy{Retries: 99}
		}, "retries must be between"},
		{"edge into trigger", func(g *WorkflowGraph) {
			g.Edges[1] = GraphEdge{From: "b", To: "start"}
		}, "trigger block must not have incoming"},
		{"disconnected node", func(g *WorkflowGraph) {
			g.Nodes = append(g.Nodes, GraphNode{ID: "island", Type: "command.reboot"})
		}, "connected to the trigger"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := chainGraph()
			tt.mutate(g)
			err := g.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestGraphCommandSteps(t *testing.T) {
	g := chainGraph()
	g.Nodes[1].Name = "First"
	g.Nodes[1].Params = map[string]interface{}{"delay_seconds": float64(3)}

	steps, err := g.CommandSteps()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Command.Slug != "reboot" || steps[0].Order != 0 || steps[0].Name != "First" {
		t.Errorf("unexpected step 0: %+v", steps[0])
	}
	if steps[0].Command.Payload["delay_seconds"] != float64(3) {
		t.Errorf("params not carried into payload: %+v", steps[0].Command.Payload)
	}
	if steps[1].Command.Slug != "status_check" || steps[1].Order != 1 {
		t.Errorf("unexpected step 1: %+v", steps[1])
	}
}

func TestGraphFromStepsRoundTrip(t *testing.T) {
	steps := []WorkflowStep{
		{Order: 1, Command: CommandSpec{Slug: "status_check", Payload: map[string]interface{}{}}},
		{Order: 0, Name: "boot", Command: CommandSpec{Slug: "reboot", Payload: map[string]interface{}{"delay_seconds": float64(1)}}},
	}

	g := GraphFromSteps(steps)
	if err := g.Validate(); err != nil {
		t.Fatalf("synthesized graph must validate: %v", err)
	}

	// One start + two command nodes, chain honors step order.
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("unexpected graph shape: %d nodes, %d edges", len(g.Nodes), len(g.Edges))
	}

	roundTrip, err := g.CommandSteps()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if roundTrip[0].Command.Slug != "reboot" || roundTrip[1].Command.Slug != "status_check" {
		t.Errorf("step order lost in round trip: %+v", roundTrip)
	}
	if roundTrip[0].Name != "boot" {
		t.Errorf("step name lost in round trip: %+v", roundTrip[0])
	}
}

func TestNormalizeSyncsStepsFromGraph(t *testing.T) {
	tpl := &WorkflowTemplate{
		Name:   "graph template",
		Status: StatusActive,
		Graph:  chainGraph(),
		// Client-sent steps are ignored: graph is the source of truth.
		Steps: []WorkflowStep{{Order: 0, Command: CommandSpec{Slug: "shutdown"}}},
	}

	if err := tpl.Normalize(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tpl.Steps) != 2 || tpl.Steps[0].Command.Slug != "reboot" {
		t.Fatalf("steps not derived from graph: %+v", tpl.Steps)
	}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("normalized template must validate: %v", err)
	}
}

func TestEffectiveGraphForLegacyTemplate(t *testing.T) {
	tpl := &WorkflowTemplate{
		Name:  "legacy",
		Steps: []WorkflowStep{{Order: 0, Command: CommandSpec{Slug: "reboot"}}},
	}
	g := tpl.EffectiveGraph()
	if g == nil || len(g.CommandNodes()) != 1 {
		t.Fatalf("legacy template must synthesize a graph: %+v", g)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("synthesized graph must validate: %v", err)
	}
}

// A schedule the platform cannot fire is refused at SAVE, where the author can
// still fix it. Before this, `99 * * * *` saved cleanly and the run simply
// never happened — the matcher resolved an out-of-range field to "matches
// nothing", which looks exactly like a correct schedule whose time has not come.
func TestValidateRejectsAnUnfireableCron(t *testing.T) {
	g := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{
			{ID: "start", Type: workflow_block.TypeSchedule, Params: map[string]interface{}{"cron": "99 * * * *"}},
			{ID: "n1", Type: "command.reboot"},
		},
		Edges: []GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "n1"}},
	}

	if err := g.Validate(); err == nil {
		t.Fatal("Validate() = nil, want the unfireable cron refused")
	}
	// A draft is allowed unfilled required params, but not a cron that is
	// present and cannot fire.
	if err := g.ValidateDraft(); err == nil {
		t.Error("ValidateDraft() = nil, want the same refusal for a present-but-bad cron")
	}
}

// A draft with no cron yet must still save.
func TestValidateDraftAcceptsABlankCron(t *testing.T) {
	g := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{
			{ID: "start", Type: workflow_block.TypeSchedule, Params: map[string]interface{}{"cron": ""}},
			{ID: "n1", Type: "command.reboot"},
		},
		Edges: []GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "n1"}},
	}
	if err := g.ValidateDraft(); err != nil {
		t.Errorf("ValidateDraft() = %v, want nil", err)
	}
}
