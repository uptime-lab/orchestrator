package workflow_execution

import (
	"context"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// branchAndRejoinRun builds the simplest graph that used to strand a join: a
// condition whose taken port reaches the join directly, while the untaken port
// still has a block on it.
//
//	cond --true--> join --> tail
//	cond --false-> other -> join
func branchAndRejoinRun() *engineRun {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "cond", Type: workflow_block.TypeCondition},
			{ID: "other", Type: workflow_block.TypeWait},
			{ID: "join", Type: workflow_block.TypeJoin},
			{ID: "tail", Type: workflow_block.TypeJoin},
		},
		Edges: []workflow_template.GraphEdge{
			{From: "cond", To: "join", FromPort: workflow_block.PortTrue},
			{From: "cond", To: "other", FromPort: workflow_block.PortFalse},
			{From: "other", To: "join"},
			{From: "join", To: "tail"},
		},
	}
	return &engineRun{
		svc:      liveEngineService(),
		ctx:      context.Background(),
		graph:    graph,
		execNode: &wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{}},
	}
}

func TestCompleteAndAdvanceEntersJoinOnBranchAndRejoin(t *testing.T) {
	r := branchAndRejoinRun()

	r.completeAndAdvance("cond", map[string]string{"result": "true"}, workflow_block.PortTrue)

	if got := r.execNode.GraphState["other"].Status; got != wen.GraphNodeSkipped {
		t.Errorf("untaken branch: status = %q, want %q", got, wen.GraphNodeSkipped)
	}
	if got := r.execNode.GraphState["join"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("join: status = %q, want %q (the join was never entered)", got, wen.GraphNodeCompleted)
	}
	if got := r.execNode.GraphState["tail"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("block after the join: status = %q, want %q", got, wen.GraphNodeCompleted)
	}
	if stalled := r.stalledBlocks(); len(stalled) != 0 {
		t.Errorf("stalledBlocks() = %v, want none", stalled)
	}
}

func TestStalledBlocksDetectsAnUnenteredBlock(t *testing.T) {
	r := branchAndRejoinRun()
	// A stall as it looked before the ordering fix: the condition completed on
	// the true port, nothing is running, and the join was never entered.
	r.execNode.GraphState["cond"] = wen.GraphNodeState{
		Status:     wen.GraphNodeCompleted,
		ChosenPort: workflow_block.PortTrue,
	}
	r.execNode.GraphState["other"] = wen.GraphNodeState{Status: wen.GraphNodeSkipped}

	stalled := r.stalledBlocks()
	if len(stalled) != 1 || stalled[0] != "join" {
		t.Fatalf("stalledBlocks() = %v, want [join]", stalled)
	}
}

func TestStalledBlocksIgnoresUntakenAndUnreachedBranches(t *testing.T) {
	r := branchAndRejoinRun()
	// The condition chose false, so the true-port successor is not "reachable
	// but never entered" — it is correctly dead. Nothing else has run yet.
	r.execNode.GraphState["cond"] = wen.GraphNodeState{
		Status:     wen.GraphNodeCompleted,
		ChosenPort: workflow_block.PortFalse,
	}
	r.execNode.GraphState["other"] = wen.GraphNodeState{Status: wen.GraphNodeRunning}

	if stalled := r.stalledBlocks(); len(stalled) != 0 {
		t.Errorf("stalledBlocks() = %v, want none", stalled)
	}
}

func TestStalledBlocksTreatsAnEmptyChosenPortAsTheDefaultPort(t *testing.T) {
	// The trigger and an on_error=continue settle both complete with no chosen
	// port and advance on "out"; a successor left stateless there is a stall.
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "first", Type: workflow_block.TypeJoin},
			{ID: "second", Type: workflow_block.TypeJoin},
		},
		Edges: []workflow_template.GraphEdge{{From: "first", To: "second"}},
	}
	r := &engineRun{
		graph: graph,
		execNode: &wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{
			"first": {Status: wen.GraphNodeCompleted},
		}},
	}

	stalled := r.stalledBlocks()
	if len(stalled) != 1 || stalled[0] != "second" {
		t.Fatalf("stalledBlocks() = %v, want [second]", stalled)
	}
}

// crossPassJoinRun reproduces a cross-pass join: a join fed by an agent block A that
// completes in an EARLY pass, and by a condition branch (cond--true-->d-->join)
// that is skipped in a LATER pass. When A completed, arrive(join) returned
// because d was still pending; when the condition later routes false and d is
// skipped, nothing calls arrive(join) again — so without the fix the join is
// stranded as "graph stalled".
//
//	A ----------------> join --> tail
//	cond --true--> d --> join
//	cond --false-> e
func crossPassJoinRun() *engineRun {
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "a", Type: workflow_block.TypeJoin}, // stand-in for a completed agent block
			{ID: "cond", Type: workflow_block.TypeCondition},
			{ID: "d", Type: workflow_block.TypeJoin},
			{ID: "e", Type: workflow_block.TypeJoin},
			{ID: "join", Type: workflow_block.TypeJoin},
			{ID: "tail", Type: workflow_block.TypeJoin},
		},
		Edges: []workflow_template.GraphEdge{
			{From: "a", To: "join"},
			{From: "cond", To: "d", FromPort: workflow_block.PortTrue},
			{From: "cond", To: "e", FromPort: workflow_block.PortFalse},
			{From: "d", To: "join"},
			{From: "join", To: "tail"},
		},
	}
	return &engineRun{
		svc:      liveEngineService(),
		ctx:      context.Background(),
		graph:    graph,
		execNode: &wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{}},
	}
}

func TestJoinEnteredWhenAPredecessorCompletedInAnEarlierPass(t *testing.T) {
	r := crossPassJoinRun()

	// Pass 1: A completed and advanced to the join, which could not enter because
	// the condition branch (d) was still pending.
	r.execNode.GraphState["a"] = wen.GraphNodeState{Status: wen.GraphNodeCompleted, ChosenPort: workflow_block.DefaultOutputPort}
	r.arrive("join")
	if got := r.execNode.GraphState["join"].Status; got != "" {
		t.Fatalf("join should still be waiting on d, got %q", got)
	}

	// Pass 2: the condition evaluates false, skipping d. This must now enter the
	// join rather than strand it.
	r.completeAndAdvance("cond", map[string]string{"result": "false"}, workflow_block.PortFalse)

	if got := r.execNode.GraphState["d"].Status; got != wen.GraphNodeSkipped {
		t.Errorf("d: status = %q, want %q", got, wen.GraphNodeSkipped)
	}
	if got := r.execNode.GraphState["join"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("join: status = %q, want %q (it was stranded)", got, wen.GraphNodeCompleted)
	}
	if got := r.execNode.GraphState["tail"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("tail after the join: status = %q, want %q", got, wen.GraphNodeCompleted)
	}
	if stalled := r.stalledBlocks(); len(stalled) != 0 {
		t.Errorf("stalledBlocks() = %v, want none", stalled)
	}
}

// liveEngineService is the minimum a run needs now that startBlock re-reads the
// stored row before it starts anything: a repository that answers "still
// running". These graphs are about join semantics, not about cancellation.
func liveEngineService() *Service {
	return &Service{
		logger:            slog.Default(),
		executionNodeRepo: &storedNodeRepo{status: wen.StatusRunning},
	}
}
