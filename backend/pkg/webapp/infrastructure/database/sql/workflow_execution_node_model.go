package sql

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// WorkflowExecutionNodeModel is the SQL representation of a workflow execution node
type WorkflowExecutionNodeModel struct {
	ID               uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	ExecutionID      uuid.UUID  `gorm:"column:execution_id;type:uuid;index;not null"`
	NodeID           uuid.UUID  `gorm:"column:node_id;type:uuid;index;not null"`
	AccountID        uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	CurrentStepIndex int        `gorm:"column:current_step_index;not null;default:-1"`
	Status           string     `gorm:"column:status;size:50;not null;default:'pending'"`
	CurrentCommandID *uuid.UUID `gorm:"column:current_command_id;type:uuid"`
	ErrorMessage     string     `gorm:"column:error_message;type:text"`
	GraphState       []byte     `gorm:"column:graph_state;type:jsonb"` // per-graph-node run state JSON
	StartedAt        *time.Time `gorm:"column:started_at;type:timestamp"`
	CompletedAt      *time.Time `gorm:"column:completed_at;type:timestamp"`
	CreatedAt        time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (WorkflowExecutionNodeModel) TableName() string {
	return "workflow_execution_nodes"
}

// encryptGraphStateValues encrypts the block-authored strings in a graph state:
// every output value, and the block's error text.
//
// Outputs carry read_file contents, run_script stdout and HTTP response bodies
// — exactly the material the API redacts behind secret.read_value — so the
// database must not keep them in the clear. ERROR text is the same
// bytes arriving by the other door: an agent echoes params back when something
// fails (a rejected value, a tool's stderr), so a resolved {{secrets.*}} lands
// there just as readily. Leaving it clear beside a sealed Outputs put it in the
// database dump and, where GORM logs interpolated statements, in the
// container logs.
//
// This changes nothing an API caller sees: the model decrypts on read, and
// RedactGraphState still shows error text to a caller below secret.read_value
// (except on a secret-fed block), because that text is what an operator triages
// a failure with. The question here is only what sits on disk.
//
// The JSON structure (statuses, timestamps, ports) stays plaintext and
// queryable. Best-effort per value: a value that fails to encrypt is dropped
// rather than stored plainly. preservePrefixed carries the same meaning as in
// encryptCommandPayload: the backfill sweeps raw rows where the
// prefix marks our own ciphertext, the write path encrypts domain values where
// a prefixed string is caller plaintext that happens to start with the token.
//
// No rewrap change is needed: rewrapGraphState walks the raw JSON for every
// encv1: token, whatever field it sits in.
func encryptGraphStateValues(state map[string]workflow_execution_node.GraphNodeState, enc commandFieldEncryptor, preservePrefixed bool) map[string]workflow_execution_node.GraphNodeState {
	if state == nil || enc == nil {
		return state
	}
	seal := func(v string) (string, bool) {
		if v == "" || (preservePrefixed && strings.HasPrefix(v, commandCipherPrefix)) {
			return v, true
		}
		ciphertext, err := enc.Encrypt(v)
		if err != nil {
			return "", false
		}
		return commandCipherPrefix + ciphertext, true
	}
	out := make(map[string]workflow_execution_node.GraphNodeState, len(state))
	for id, ns := range state {
		if len(ns.Outputs) > 0 {
			encOut := make(map[string]string, len(ns.Outputs))
			for k, v := range ns.Outputs {
				if sealed, ok := seal(v); ok {
					encOut[k] = sealed
				}
				// else: drop rather than persist in the clear
			}
			ns.Outputs = encOut
		}
		if sealed, ok := seal(ns.Error); ok {
			ns.Error = sealed
		} else {
			// Dropping the text loses the failure's explanation, which is worse
			// here than for one output among several — but storing it plainly is
			// the thing this function exists to prevent, and the STATUS still
			// says the block failed.
			ns.Error = ""
		}
		out[id] = ns
	}
	return out
}

// decryptGraphStateValues reverses encryptGraphStateValues. Unprefixed values
// are legacy plaintext and pass through; undecryptable values are dropped
// rather than surfaced as ciphertext.
func decryptGraphStateValues(state map[string]workflow_execution_node.GraphNodeState, enc commandFieldEncryptor) map[string]workflow_execution_node.GraphNodeState {
	if state == nil || enc == nil {
		return state
	}
	open := func(v string) (string, bool) {
		if !strings.HasPrefix(v, commandCipherPrefix) {
			return v, true // legacy plaintext
		}
		plain, err := enc.Decrypt(strings.TrimPrefix(v, commandCipherPrefix))
		if err != nil {
			return "", false
		}
		return plain, true
	}
	for id, ns := range state {
		for k, v := range ns.Outputs {
			plain, ok := open(v)
			if !ok {
				delete(ns.Outputs, k)
				continue
			}
			ns.Outputs[k] = plain
		}
		if plain, ok := open(ns.Error); ok {
			ns.Error = plain
		} else {
			// Ciphertext in an error message would be read as the failure.
			ns.Error = ""
		}
		state[id] = ns
	}
	return state
}

// ToDomain converts the SQL model to a domain model, decrypting block outputs.
//
// An unreadable graph_state is an ERROR, not an empty state. Left nil on a
// RUNNING row it defeats the stall guard exactly: finalize finds nothing
// running, stalledBlocks finds nothing reachable-but-unentered (no block is
// completed to have taken an edge), and the run is reported COMPLETED having
// executed none of the blocks the operator wrote — which is the one outcome
// that guard exists to prevent.
func (m *WorkflowExecutionNodeModel) ToDomain(enc commandFieldEncryptor) (*workflow_execution_node.WorkflowExecutionNode, error) {
	domain := &workflow_execution_node.WorkflowExecutionNode{
		ID:               m.ID,
		ExecutionID:      m.ExecutionID,
		NodeID:           m.NodeID,
		AccountID:        m.AccountID,
		CurrentStepIndex: m.CurrentStepIndex,
		Status:           workflow_execution_node.WorkflowExecutionNodeStatus(m.Status),
		CurrentCommandID: m.CurrentCommandID,
		ErrorMessage:     m.ErrorMessage,
		StartedAt:        m.StartedAt,
		CompletedAt:      m.CompletedAt,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}

	if len(m.GraphState) > 0 {
		var graphState map[string]workflow_execution_node.GraphNodeState
		if err := json.Unmarshal(m.GraphState, &graphState); err != nil {
			return nil, fmt.Errorf("execution node %s has unreadable graph state: %w", m.ID, err)
		}
		domain.GraphState = decryptGraphStateValues(graphState, enc)
	}

	return domain, nil
}

// FromDomain updates the SQL model from a domain model, encrypting block
// outputs on the way to storage. A marshal failure is returned rather than
// swallowed: silently storing NULL over a live graph state re-enters the run
// as if it had never started.
func (m *WorkflowExecutionNodeModel) FromDomain(node *workflow_execution_node.WorkflowExecutionNode, enc commandFieldEncryptor) error {
	m.ID = node.ID
	m.ExecutionID = node.ExecutionID
	m.NodeID = node.NodeID
	m.AccountID = node.AccountID
	m.CurrentStepIndex = node.CurrentStepIndex
	m.Status = string(node.Status)
	m.CurrentCommandID = node.CurrentCommandID
	m.ErrorMessage = node.ErrorMessage
	m.StartedAt = node.StartedAt
	m.CompletedAt = node.CompletedAt
	m.CreatedAt = node.CreatedAt
	m.UpdatedAt = node.UpdatedAt

	m.GraphState = nil
	if len(node.GraphState) > 0 {
		graphStateJSON, err := json.Marshal(encryptGraphStateValues(node.GraphState, enc, false))
		if err != nil {
			return fmt.Errorf("serializing execution node %s graph state: %w", node.ID, err)
		}
		m.GraphState = graphStateJSON
	}
	return nil
}
