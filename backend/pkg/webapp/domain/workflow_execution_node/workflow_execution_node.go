package workflow_execution_node

import (
	"time"

	"github.com/google/uuid"
)

// WorkflowExecutionNodeStatus represents the status of a node's workflow execution
type WorkflowExecutionNodeStatus string

const (
	StatusPending   WorkflowExecutionNodeStatus = "pending"   // Created but not started
	StatusRunning   WorkflowExecutionNodeStatus = "running"   // Currently executing steps
	StatusCompleted WorkflowExecutionNodeStatus = "completed" // All steps completed successfully
	StatusFailed    WorkflowExecutionNodeStatus = "failed"    // A step failed, execution stopped
)

// String returns the string representation of the status
func (s WorkflowExecutionNodeStatus) String() string {
	return string(s)
}

// GraphNodeStatus is the run status of one graph block on one node.
type GraphNodeStatus string

const (
	GraphNodeRunning   GraphNodeStatus = "running"
	GraphNodeCompleted GraphNodeStatus = "completed"
	GraphNodeFailed    GraphNodeStatus = "failed"
	// GraphNodeSkipped marks a block on a branch that was not taken (the
	// condition chose the other port, or an ancestor was skipped). Terminal
	// and non-blocking: a join treats a skipped predecessor as settled.
	GraphNodeSkipped GraphNodeStatus = "skipped"
)

// GraphNodeState records the run state of a single graph block; keyed by the
// block's graph node ID in WorkflowExecutionNode.GraphState. Blocks that have
// not started have no entry (implicitly pending).
type GraphNodeState struct {
	Status      GraphNodeStatus `json:"status"`
	CommandID   *uuid.UUID      `json:"command_id,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Error       string          `json:"error,omitempty"`
	// Outputs a completed block exposes to later blocks via
	// {{blocks.<id>.outputs.<key>}}. Stringified for a simple jsonb shape.
	Outputs map[string]string `json:"outputs,omitempty"`
	// Attempts counts how many times the block has been dispatched (retries).
	Attempts int `json:"attempts,omitempty"`
	// ChosenPort is the output port a completed control block routed to
	// (e.g. a condition's "true"/"false"); empty means the default port.
	ChosenPort string `json:"chosen_port,omitempty"`
	// WaitUntil is set on a running control.wait block; the engine completes
	// it once the clock passes this time.
	WaitUntil *time.Time `json:"wait_until,omitempty"`
}

// WorkflowExecutionNode represents the execution of a workflow for a specific node
type WorkflowExecutionNode struct {
	ID               uuid.UUID
	ExecutionID      uuid.UUID
	NodeID           uuid.UUID
	AccountID        uuid.UUID
	CurrentStepIndex int
	Status           WorkflowExecutionNodeStatus
	CurrentCommandID *uuid.UUID
	ErrorMessage     string
	// GraphState tracks per-block run state keyed by graph node ID; the
	// canvas run view colors blocks from it.
	GraphState  map[string]GraphNodeState
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewWorkflowExecutionNode creates a new workflow execution node
func NewWorkflowExecutionNode(executionID, nodeID, accountID uuid.UUID) *WorkflowExecutionNode {
	now := time.Now()
	return &WorkflowExecutionNode{
		ID:               uuid.New(),
		ExecutionID:      executionID,
		NodeID:           nodeID,
		AccountID:        accountID,
		CurrentStepIndex: -1, // -1 means not started
		Status:           StatusPending,
		CurrentCommandID: nil,
		ErrorMessage:     "",
		StartedAt:        nil,
		CompletedAt:      nil,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// StartStep marks the start of a step execution
func (wen *WorkflowExecutionNode) StartStep(stepIndex int, commandID uuid.UUID) {
	now := time.Now()
	if wen.Status == StatusPending {
		wen.Status = StatusRunning
		wen.StartedAt = &now
	}
	wen.CurrentStepIndex = stepIndex
	wen.CurrentCommandID = &commandID
	wen.UpdatedAt = now
}

// CompleteStep marks the completion of a step and prepares for next
func (wen *WorkflowExecutionNode) CompleteStep() {
	wen.CurrentCommandID = nil // Clear current command
	wen.UpdatedAt = time.Now()
}

// Complete marks the node execution as completed successfully
func (wen *WorkflowExecutionNode) Complete() {
	now := time.Now()
	wen.Status = StatusCompleted
	wen.CompletedAt = &now
	wen.CurrentCommandID = nil
	wen.UpdatedAt = now
}

// Fail marks the node execution as failed
func (wen *WorkflowExecutionNode) Fail(errorMessage string) {
	now := time.Now()
	wen.Status = StatusFailed
	wen.CompletedAt = &now
	wen.ErrorMessage = errorMessage
	wen.CurrentCommandID = nil
	wen.UpdatedAt = now
}

// MarkGraphNodeRunning records that a graph block started executing.
func (wen *WorkflowExecutionNode) MarkGraphNodeRunning(graphNodeID string, commandID uuid.UUID) {
	if graphNodeID == "" {
		return
	}
	if wen.GraphState == nil {
		wen.GraphState = make(map[string]GraphNodeState)
	}
	now := time.Now()
	wen.GraphState[graphNodeID] = GraphNodeState{
		Status:    GraphNodeRunning,
		CommandID: &commandID,
		StartedAt: &now,
	}
}

// MarkGraphNodeCompleted records that a graph block finished successfully.
func (wen *WorkflowExecutionNode) MarkGraphNodeCompleted(graphNodeID string) {
	wen.finishGraphNode(graphNodeID, GraphNodeCompleted, "")
}

// MarkGraphNodeFailed records that a graph block failed.
func (wen *WorkflowExecutionNode) MarkGraphNodeFailed(graphNodeID string, errorMessage string) {
	wen.finishGraphNode(graphNodeID, GraphNodeFailed, errorMessage)
}

// FailRunningGraphNodes fails every block still marked running, for a run that
// is being stopped from outside the engine, and returns the commands those
// blocks were waiting on. Without it the run view shows the block running for
// good: the engine that would have finished it never looks at a settled row.
//
// The map is replaced, not edited in place: a copy of this struct shares it,
// and a settle that loses its claim must leave the caller's copy as it read it.
func (wen *WorkflowExecutionNode) FailRunningGraphNodes(errorMessage string) []uuid.UUID {
	if wen.GraphState == nil {
		return nil
	}
	var commandIDs []uuid.UUID
	states := make(map[string]GraphNodeState, len(wen.GraphState))
	for id, state := range wen.GraphState {
		states[id] = state
	}
	wen.GraphState = states
	for id, state := range states {
		if state.Status != GraphNodeRunning {
			continue
		}
		if state.CommandID != nil {
			commandIDs = append(commandIDs, *state.CommandID)
		}
		wen.finishGraphNode(id, GraphNodeFailed, errorMessage)
	}
	return commandIDs
}

func (wen *WorkflowExecutionNode) finishGraphNode(graphNodeID string, status GraphNodeStatus, errorMessage string) {
	if graphNodeID == "" {
		return
	}
	if wen.GraphState == nil {
		wen.GraphState = make(map[string]GraphNodeState)
	}
	state := wen.GraphState[graphNodeID]
	now := time.Now()
	state.Status = status
	state.CompletedAt = &now
	state.Error = errorMessage
	if state.StartedAt == nil {
		state.StartedAt = &now
	}
	wen.GraphState[graphNodeID] = state
}
