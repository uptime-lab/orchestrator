package node_commands

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
)

// NodeCommandClientChannel implements NodeCommandClient using a direct channel
type NodeCommandClientChannel struct {
	channel chan node_commands.NodeCommandOperation
}

// Send sends an operation to the client through the channel
func (c *NodeCommandClientChannel) Send(op node_commands.NodeCommandOperation) error {
	c.channel <- op
	return nil
}

// GetChannel returns the underlying channel
func (c *NodeCommandClientChannel) GetChannel() chan node_commands.NodeCommandOperation {
	return c.channel
}

// NewNodeCommandClientChannel creates a new channel-based NodeCommandClient
func NewNodeCommandClientChannel() *NodeCommandClientChannel {
	return &NodeCommandClientChannel{
		channel: make(chan node_commands.NodeCommandOperation, 10000),
	}
}
