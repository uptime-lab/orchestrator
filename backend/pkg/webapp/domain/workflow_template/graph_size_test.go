package workflow_template

import (
	"strings"
	"testing"
)

// The document itself needs a ceiling: a graph is validated, linearized,
// snapshotted into every execution and walked once per pass, so cost is
// superlinear in node count while the row carrying it is free to write.
//
// The number is a runaway guard, not a product limit — the test pins that a
// realistic playbook is nowhere near it.
func TestGraphSizeCap(t *testing.T) {
	build := func(n int) *WorkflowGraph {
		g := &WorkflowGraph{SchemaVersion: GraphSchemaVersion}
		g.Nodes = append(g.Nodes, GraphNode{ID: "trigger", Type: "trigger.manual"})
		for i := 1; i < n; i++ {
			g.Nodes = append(g.Nodes, GraphNode{ID: string(rune('a'+i%26)) + itoa(i), Type: "control.join"})
		}
		return g
	}

	if err := build(MaxGraphNodes + 1).Validate(); err == nil || !strings.Contains(err.Error(), "node limit") {
		t.Fatalf("a graph over the cap must be refused by size, got %v", err)
	}
	// A realistic playbook must not be anywhere near the ceiling.
	if err := build(40).Validate(); err != nil && strings.Contains(err.Error(), "limit") {
		t.Fatalf("a 40-node playbook hit a size limit: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
