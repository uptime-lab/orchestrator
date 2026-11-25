package results

import (
	"time"
)

// NodeCommandResult represents a node command in API responses
type NodeCommandResult struct {
	ID        string                 `json:"id"`
	NodeID    string                 `json:"node_id"`
	AccountID string                 `json:"account_id"`
	Slug      string                 `json:"slug"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Status    string                 `json:"status"`
	Progress  int                    `json:"progress"`
	Details   string                 `json:"details,omitempty"`
	// Result carries the structured outputs the agent reported — run_script's
	// exit_code/stdout/stderr, read_file's content. Same bytes the graph state
	// exposes as block outputs, and redacted the same way (see PublicCommandResult).
	Result                  map[string]string `json:"result,omitempty"`
	WorkflowExecutionNodeID *string           `json:"workflow_execution_node_id,omitempty"`
	CreatedAt               time.Time         `json:"created_at"`
	UpdatedAt               time.Time         `json:"updated_at"`
}

// NodeCommandListResult represents a paginated list of node commands
type NodeCommandListResult struct {
	Commands []NodeCommandResult `json:"commands"`
	Total    int                 `json:"total"`
	Limit    int                 `json:"limit"`
	Offset   int                 `json:"offset"`
}
