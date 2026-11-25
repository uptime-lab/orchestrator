package node_commands

import (
	"github.com/google/uuid"
)

// OperationType represents the type of node command operation
type OperationType string

const (
	OperationTypeSendCommand OperationType = "send_command" // For gRPC server to send commands to agents
)

// NodeCommandOperation represents an operation on node commands
type NodeCommandOperation struct {
	Type OperationType

	// For SendCommand
	SendCommand *SendCommandOperation
}

// SendCommandOperation represents a request to send a registered command to an agent
type SendCommandOperation struct {
	NodeCommandID uuid.UUID
	NodeID        uuid.UUID
	AccountID     uuid.UUID
}
