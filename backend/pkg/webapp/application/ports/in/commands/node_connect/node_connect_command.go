package node_connect

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/google/uuid"
)

// NodeConnectCommand represents a command to connect a node with hardware fingerprint
type NodeConnectCommand struct {
	NodeID      uuid.UUID
	AccountID   uuid.UUID
	UniqueToken string
	Fingerprint node.HardwareFingerprint
}
