package grpc

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// runningVersionNodeService answers the send-time read of the node's agent
// version; anything else panics through the nil embed.
type runningVersionNodeService struct {
	services.NodeService
	running string
}

func (s runningVersionNodeService) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: nodeID, AccountID: accountID, AgentVersion: s.running}, nil
}

// The wire's only force flag also lifts the agent's anti-rollback check. A
// stored reinstall relaxes it only for the version the node runs at the
// moment of the send: a queued reinstall delivered after the node moved to a
// newer agent would otherwise be a downgrade nobody asked for.
func TestAReinstallForcesOnlyTheVersionTheNodeRunsAtSendTime(t *testing.T) {
	for _, c := range []struct {
		name, requested, running string
		want                     bool
	}{
		{"the same version is reinstalled", "0.8.1", "0.8.1", true},
		{"a node that moved on is not downgraded", "0.8.1", "0.8.2", false},
		{"latest is never forced", "latest", "0.8.1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := hostnameServer()
			srv.nodeService = runningVersionNodeService{running: c.running}
			cmd := &node_command.NodeCommand{
				Slug:      node_command.SlugUpdateAgent,
				AccountID: uuid.New(),
				NodeID:    uuid.New(),
				Payload:   map[string]interface{}{"agent_version": c.requested, "allow_downgrade": false, "reinstall": true},
			}
			built, err := srv.buildProtoCommandFromNodeCommand(context.Background(), cmd)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := built.GetUpdateAgent().GetAllowDowngrade(); got != c.want {
				t.Errorf("allow_downgrade = %v, want %v", got, c.want)
			}
		})
	}
}
