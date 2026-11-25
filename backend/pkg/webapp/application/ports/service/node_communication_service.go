package service

import (
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// NodeCommunicationService defines the interface for node communication
type NodeCommunicationService interface {
	// SendCommand sends a command to a specific node
	SendCommand(nodeID string, command *proto.Command) error

	// GetConnectedNodes returns a list of currently connected node IDs
	GetConnectedNodes() []string

	// IsNodeConnected checks if a node has an active gRPC connection
	IsNodeConnected(nodeID string) bool
}
