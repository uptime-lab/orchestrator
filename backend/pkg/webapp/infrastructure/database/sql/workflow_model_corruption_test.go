package sql

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// The worst of the four swallows: an unreadable graph_state used to leave
// GraphState nil on a RUNNING row. finalize then finds nothing running, and
// stalledBlocks finds nothing reachable-but-unentered — because no block is
// completed to have taken an edge — so the run is reported COMPLETED having
// executed none of the blocks the operator wrote. That is precisely the
// outcome the stall guard exists to prevent.
func TestExecutionNodeWithUnreadableGraphStateFailsTheRead(t *testing.T) {
	model := &WorkflowExecutionNodeModel{
		ID:         uuid.New(),
		Status:     string(wen.StatusRunning),
		GraphState: []byte(`{"b1": not json}`),
	}

	node, err := model.ToDomain(nil)

	if err == nil {
		t.Fatalf("ToDomain() = %+v, nil — a corrupted run must not read back as a valid one", node)
	}
	if node != nil {
		t.Errorf("node = %+v, want nil alongside the error", node)
	}
	if !strings.Contains(err.Error(), model.ID.String()) {
		t.Errorf("error = %q, want it to name the row", err)
	}
}

// Well-formed state still round-trips — the guard must not cost the ordinary read.
func TestExecutionNodeWithReadableGraphStateStillConverts(t *testing.T) {
	model := &WorkflowExecutionNodeModel{
		ID:         uuid.New(),
		Status:     string(wen.StatusRunning),
		GraphState: []byte(`{"b1":{"status":"completed"}}`),
	}

	node, err := model.ToDomain(nil)
	if err != nil {
		t.Fatalf("ToDomain() = %v, want nil", err)
	}
	if got := node.GraphState["b1"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("graph state b1 = %q, want %q", got, wen.GraphNodeCompleted)
	}
}

// An unreadable steps column used to become an empty list: a "playbook" that
// executes nothing, reported to its operator as valid.
func TestTemplateWithUnreadableStepsFailsTheRead(t *testing.T) {
	model := &WorkflowTemplateModel{ID: uuid.New(), Steps: []byte(`[{"order":`)}

	tmpl, err := model.ToDomain()

	if err == nil {
		t.Fatalf("ToDomain() = %+v, nil — an empty playbook is not a safe substitute", tmpl)
	}
}

// An unreadable graph used to become nil, which EffectiveGraph then replaces
// with a bare trigger chain synthesized from the steps — losing the schedule
// trigger, so the playbook silently stops firing.
func TestTemplateWithUnreadableGraphFailsTheRead(t *testing.T) {
	model := &WorkflowTemplateModel{
		ID:    uuid.New(),
		Steps: []byte(`[]`),
		Graph: []byte(`{"schema_version":`),
	}

	if _, err := model.ToDomain(); err == nil {
		t.Fatal("ToDomain() = nil error, want the unreadable graph reported")
	}
}

// A legacy row with no graph is not corruption — it is the documented shape.
func TestTemplateWithNoGraphStillConverts(t *testing.T) {
	model := &WorkflowTemplateModel{ID: uuid.New(), Steps: []byte(`[]`)}

	tmpl, err := model.ToDomain()
	if err != nil {
		t.Fatalf("ToDomain() = %v, want nil", err)
	}
	if tmpl.Graph != nil {
		t.Errorf("graph = %v, want nil for a legacy row", tmpl.Graph)
	}
}
