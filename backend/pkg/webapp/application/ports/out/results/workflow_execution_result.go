package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowExecutionNodeResult represents a workflow execution node in API responses
type WorkflowExecutionNodeResult struct {
	ID               string  `json:"id"`
	ExecutionID      string  `json:"execution_id"`
	NodeID           string  `json:"node_id"`
	AccountID        string  `json:"account_id"`
	CurrentStepIndex int     `json:"current_step_index"`
	Status           string  `json:"status"`
	CurrentCommandID *string `json:"current_command_id,omitempty"`
	ErrorMessage     string  `json:"error_message,omitempty"`
	// GraphState is the per-graph-block run state keyed by graph node ID
	GraphState  map[string]workflow_execution_node.GraphNodeState `json:"graph_state,omitempty"`
	StartedAt   *time.Time                                        `json:"started_at,omitempty"`
	CompletedAt *time.Time                                        `json:"completed_at,omitempty"`
	CreatedAt   time.Time                                         `json:"created_at"`
	UpdatedAt   time.Time                                         `json:"updated_at"`
}

// RedactGraphState returns a copy of a per-block run state map with block
// OUTPUTS stripped.
//
// Outputs are where read_file/run_script/wait_for_file/http_request return
// whatever they read or printed, so this map is as secret-bearing as a command
// payload and gets the same treatment.
//
// Error text is redacted for SECRET-FED BLOCKS ONLY, which is the same rule
// PublicCommandDetails applies to a command's details, and for the same reason
// in both directions.
//
// Leaving it wholly intact was the original decision, and its argument still
// holds for the ordinary case: a manager may run a playbook without holding
// secret.read_value, and turning "node offline" and "timeout after 300s" into a
// placeholder leaves them unable to triage the runs they are authorized to
// start. What that argument did not cover is the block whose params carried
// {{secrets.*}}: an agent echoes its arguments back in a failure, so the same
// bytes the per-node command endpoint replaces with a placeholder were readable
// here at playbook.read, which RoleViewer holds.
//
// So the gate follows the block, not the caller's patience: a block that never
// touched a secret keeps its error, and one that did loses it.
// hidden, when non-nil, names blocks whose whole state must be withheld rather
// than merely stripped — see RedactGraphStateForGraph.
func RedactGraphState(state map[string]workflow_execution_node.GraphNodeState) map[string]workflow_execution_node.GraphNodeState {
	return redactGraphState(state, nil, nil)
}

// RedactGraphStateForGraph is RedactGraphState plus the one thing stripping
// fields cannot fix: a block's STATUS.
//
// `completed` on one branch and `skipped` on the other is the same fact as the
// condition's redacted output — a condition comparing {{secrets.api_key}} to a
// literal answers "did it match?" through the branch that ran, and `contains`
// turns that into a character-by-character read. Outputs and ChosenPort were
// already redacted; the statuses below were not.
//
// So for a caller without secret.read_value, the state of a secret-fed
// unprivileged block and of everything downstream of it is OMITTED entirely.
// Omitted rather than blanked, for the reason the WebSocket push omits: a
// placeholder status is a value the client would render as fact.
//
// Authoring such a graph now requires the privileged tier, so this covers the
// runs of playbooks written before that gate and by admins who may write them.
func RedactGraphStateForGraph(
	state map[string]workflow_execution_node.GraphNodeState,
	graph *workflow_template.WorkflowGraph,
) map[string]workflow_execution_node.GraphNodeState {
	return redactGraphState(state, graph.BlocksDownstreamOfSecretConditions(), graph.SecretFedBlocks())
}

// secretFed names the blocks whose ERROR text may contain resolved secret
// material; hidden names the ones whose whole state must be withheld.
func redactGraphState(
	state map[string]workflow_execution_node.GraphNodeState,
	hidden map[string]bool,
	secretFed map[string]bool,
) map[string]workflow_execution_node.GraphNodeState {
	if state == nil {
		return nil
	}
	out := make(map[string]workflow_execution_node.GraphNodeState, len(state))
	for k, v := range state {
		if hidden[k] {
			continue
		}
		if len(v.Outputs) > 0 {
			redacted := make(map[string]string, len(v.Outputs))
			for key := range v.Outputs {
				redacted[key] = redactedPlaceholder
			}
			v.Outputs = redacted
		}
		// ChosenPort is the same fact as the condition block's redacted
		// outputs["result"], leaking around the redaction: a condition
		// comparing {{secrets.api_key}} to a literal answers "did it match?"
		// through the port it routed to, and repeating the run with different
		// literals reads the value out one comparison at a time. Cleared for
		// the same callers that lose Outputs, and it costs nothing today —
		// nothing in the SPA reads chosen_port.
		v.ChosenPort = ""
		// An agent echoes its arguments back in a failure, so a block that
		// resolved a secret into a param can carry it here verbatim.
		if secretFed[k] && v.Error != "" {
			v.Error = redactedPlaceholder
		}
		out[k] = v
	}
	return out
}

// NewWorkflowExecutionNodeResultForPush builds the shape sent over WebSocket.
//
// The secret-bearing fields are OMITTED rather than replaced with a placeholder.
// A placeholder is worse than nothing here: clients merge pushed frames into a
// store that may already hold the UNREDACTED values a privileged caller fetched
// over HTTP, so a "[redacted]" string would overwrite real output the user is
// entitled to and cannot get back without a refetch. An absent field lets the
// client keep what it has.
func NewWorkflowExecutionNodeResultForPush(node *workflow_execution_node.WorkflowExecutionNode) *WorkflowExecutionNodeResult {
	result := NewWorkflowExecutionNodeResult(node, false)
	result.GraphState = nil
	result.ErrorMessage = ""
	return result
}

// NewWorkflowExecutionNodeResult creates a new WorkflowExecutionNodeResult from
// a domain model.
//
// redactOutputs is an explicit parameter rather than an internal decision so a
// new caller cannot silently forget it — this result carries block outputs,
// which is exactly what leaked before. Pass true unless the caller has verified
// the recipient holds authz.ActionSecretReadValue.
func NewWorkflowExecutionNodeResult(node *workflow_execution_node.WorkflowExecutionNode, redactOutputs bool) *WorkflowExecutionNodeResult {
	return NewWorkflowExecutionNodeResultForRun(node, redactOutputs, nil)
}

// NewWorkflowExecutionNodeResultForRun is NewWorkflowExecutionNodeResult with
// the run's pinned graph, which is what lets the redaction also hide the branch
// a secret-derived condition took (see RedactGraphStateForGraph). A nil graph
// falls back to field-level redaction alone.
func NewWorkflowExecutionNodeResultForRun(
	node *workflow_execution_node.WorkflowExecutionNode,
	redactOutputs bool,
	graph *workflow_template.WorkflowGraph,
) *WorkflowExecutionNodeResult {
	graphState := node.GraphState
	if redactOutputs {
		graphState = RedactGraphStateForGraph(graphState, graph)
	}
	// ErrorMessage is usually engine text ("node offline", "timeout"), but a
	// failing BLOCK's message is copied into it verbatim (engine.go's
	// execNode.Fail(msg)) — so when that block was secret-fed, the text this
	// field carries is the one the graph state just lost. Redacted by EQUALITY
	// with that block's original error rather than by guesswork: engine text
	// stays readable, and the copy does not become a way around the redaction
	// two lines above.
	errorMessage := node.ErrorMessage
	if redactOutputs && errorMessage != "" {
		for id, st := range node.GraphState {
			if graph.SecretFedBlocks()[id] && st.Error != "" && st.Error == errorMessage {
				errorMessage = redactedPlaceholder
				break
			}
		}
	}

	result := &WorkflowExecutionNodeResult{
		ID:               node.ID.String(),
		ExecutionID:      node.ExecutionID.String(),
		NodeID:           node.NodeID.String(),
		AccountID:        node.AccountID.String(),
		CurrentStepIndex: node.CurrentStepIndex,
		Status:           string(node.Status),
		ErrorMessage:     errorMessage,
		GraphState:       graphState,
		CreatedAt:        node.CreatedAt,
		UpdatedAt:        node.UpdatedAt,
		StartedAt:        node.StartedAt,
		CompletedAt:      node.CompletedAt,
	}

	if node.CurrentCommandID != nil {
		cmdID := node.CurrentCommandID.String()
		result.CurrentCommandID = &cmdID
	}

	return result
}

// WorkflowExecutionResult represents a workflow execution in API responses
type WorkflowExecutionResult struct {
	ID          string                        `json:"id"`
	TemplateID  string                        `json:"template_id"`
	AccountID   string                        `json:"account_id"`
	Status      string                        `json:"status"`
	StartedAt   *time.Time                    `json:"started_at,omitempty"`
	CompletedAt *time.Time                    `json:"completed_at,omitempty"`
	CreatedAt   time.Time                     `json:"created_at"`
	UpdatedAt   time.Time                     `json:"updated_at"`
	Nodes       []WorkflowExecutionNodeResult `json:"nodes,omitempty"` // Optional: populated when getting execution details
	NodeCount   int                           `json:"node_count"`      // Count of nodes (stored in workflow_executions table)
	// NodeIDs are the execution's target nodes (for OU-based filtering in lists)
	NodeIDs []string `json:"node_ids,omitempty"`
	// TemplateSnapshot is the template definition pinned when the execution started
	TemplateSnapshot *workflow_template.TemplateSnapshot `json:"template_snapshot,omitempty"`
}

// WorkflowExecutionListResult is the paged envelope of the executions list.
type WorkflowExecutionListResult struct {
	Executions []*WorkflowExecutionResult `json:"executions"`
	Count      int                        `json:"count"` // rows in this page
	Total      int64                      `json:"total"` // rows matching the filter
	Page       int                        `json:"page"`
	PerPage    int                        `json:"per_page"`
}

// NewWorkflowExecutionListResult builds the paged list envelope.
func NewWorkflowExecutionListResult(executions []*WorkflowExecutionResult, total int64, page, perPage int) *WorkflowExecutionListResult {
	return &WorkflowExecutionListResult{
		Executions: executions,
		Count:      len(executions),
		Total:      total,
		Page:       page,
		PerPage:    perPage,
	}
}

// NewWorkflowExecutionResult creates a new WorkflowExecutionResult from a domain model
func NewWorkflowExecutionResult(execution *workflow_execution.WorkflowExecution) *WorkflowExecutionResult {
	nodeIDs := make([]string, 0, len(execution.NodeIDs))
	for _, id := range execution.NodeIDs {
		nodeIDs = append(nodeIDs, id.String())
	}
	return &WorkflowExecutionResult{
		ID:               execution.ID.String(),
		TemplateID:       execution.TemplateID.String(),
		AccountID:        execution.AccountID.String(),
		Status:           string(execution.Status),
		StartedAt:        execution.StartedAt,
		CompletedAt:      execution.CompletedAt,
		CreatedAt:        execution.CreatedAt,
		UpdatedAt:        execution.UpdatedAt,
		NodeCount:        execution.NodeCount,
		NodeIDs:          nodeIDs,
		TemplateSnapshot: execution.TemplateSnapshot,
	}
}
