package node_command

import (
	"errors"

	"github.com/google/uuid"
)

// Skip reasons distinguish "we deliberately did not try" from "we tried and it
// failed". Reporting both as a bare failure at fleet scale sends the operator
// hunting for a bug when the answer is "update the agent".
const (
	SkipReasonAgentTooOld = "agent_too_old"
	// SkipReasonAgentAlreadyCurrent: update_agent named the version the node
	// already runs, so updating would only restart its agent.
	SkipReasonAgentAlreadyCurrent = "agent_already_current"
)

// NodeCommandResultItem represents the result of sending a command to a single node
type NodeCommandResultItem struct {
	NodeID    uuid.UUID  `json:"node_id"`
	Success   bool       `json:"success"`
	CommandID *uuid.UUID `json:"command_id,omitempty"` // Only present if successful
	Error     string     `json:"error,omitempty"`      // Only present if failed
	// Err carries the original typed error (never serialized) so the single-node
	// SendCommand path can preserve error identity — e.g. CommandPayloadTooLargeError
	// or AgentTooOldError — for the handler's errors.As mapping, which the string
	// Error field above flattens away.
	Err error `json:"-"`
	// Queued marks a command accepted for a node that is currently offline: it
	// waits and is delivered on reconnect. Success is true, but nothing has been
	// applied yet — callers must not report it as done.
	Queued bool `json:"queued,omitempty"`
	// Skipped marks a node deliberately not attempted, with SkipReason naming
	// which precondition it failed.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// FailureError returns the error this item failed with, with its TYPE intact.
//
// Every caller that unwraps a single-node batch needs this, and every caller
// that reached for `Error` instead reintroduced the bug the `Err` field exists
// to prevent: a typed refusal rebuilt as a string stops matching errors.As, and
// the handler answers 500 "the platform broke" instead of the refusal's own
// status and remedy. Six call sites did exactly that while three did not, so
// the answer lives here once rather than in each of them.
//
// Returns nil when the item did not fail.
func (i NodeCommandResultItem) FailureError() error {
	if i.Success {
		return nil
	}
	if i.Err != nil {
		return i.Err
	}
	if i.Error != "" {
		return errors.New(i.Error)
	}
	return errors.New("the command was refused, and no reason was recorded")
}

// BatchCommandResult represents the result of sending commands to multiple nodes
type BatchCommandResult struct {
	Results    []NodeCommandResultItem `json:"results"`
	TotalCount int                     `json:"total_count"`
	// SuccessCount counts commands ACCEPTED by the platform, which includes
	// queued ones. It does not mean the command ran on the node.
	SuccessCount int `json:"success_count"`
	FailureCount int `json:"failure_count"`
	// QueuedCount and SkippedCount break the total down so callers can render
	// "accepted / queued / skipped" instead of implying everything landed.
	QueuedCount     int            `json:"queued_count"`
	SkippedCount    int            `json:"skipped_count"`
	SkippedByReason map[string]int `json:"skipped_by_reason,omitempty"`
}

// NewBatchCommandResult creates a new BatchCommandResult
func NewBatchCommandResult(results []NodeCommandResultItem) *BatchCommandResult {
	successCount := 0
	queuedCount := 0
	skippedCount := 0
	skippedByReason := map[string]int{}
	for _, result := range results {
		if result.Success {
			successCount++
		}
		if result.Queued {
			queuedCount++
		}
		if result.Skipped {
			skippedCount++
			if result.SkipReason != "" {
				skippedByReason[result.SkipReason]++
			}
		}
	}
	if len(skippedByReason) == 0 {
		skippedByReason = nil
	}

	return &BatchCommandResult{
		Results:         results,
		TotalCount:      len(results),
		SuccessCount:    successCount,
		FailureCount:    len(results) - successCount,
		QueuedCount:     queuedCount,
		SkippedCount:    skippedCount,
		SkippedByReason: skippedByReason,
	}
}
