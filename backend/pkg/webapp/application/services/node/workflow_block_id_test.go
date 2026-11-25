package node

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// capturingCmdService records the command the dispatch pipeline registers, so a
// test can read the payload that would be stored.
type capturingCmdService struct {
	services.NodeCommandService
	registered *node_command.NodeCommand
}

func (f *capturingCmdService) FindActiveDeviceCommand(context.Context, uuid.UUID) (*node_command.NodeCommand, error) {
	return nil, nil
}

func (f *capturingCmdService) RegisterNodeCommandWithWorkflowNode(_ context.Context, nodeID uuid.UUID, slug string, payload map[string]interface{}, _ interface{}, _ *uuid.UUID) (*node_command.NodeCommand, error) {
	cmd := &node_command.NodeCommand{ID: uuid.New(), NodeID: nodeID, Slug: slug, Payload: payload}
	f.registered = cmd
	return cmd, nil
}

// blockIDService wires just enough for a queueable dispatch to reach
// registration: the node is offline, set_hostname queues, and the capturing
// service reads back the payload that would be stored.
func blockIDService(cmdSvc *capturingCmdService, accountID uuid.UUID) (*Service, *node.Node) {
	n := &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		Status:         node.StatusOffline,
		ApprovalStatus: node.ApprovalStatusApproved,
		AgentVersion:   "0.6.13",
	}
	return &Service{
		nodeRepo:             &payloadBoundRepo{n: n},
		communicationService: payloadBoundComm{},
		nodeCommandService:   cmdSvc,
		logger:               slog.Default(),
	}, n
}

// The block id is what lets the engine reconcile a pass lost between dispatch
// and finalize instead of dispatching the block twice. It must land
// in the stored payload — and ONLY for engine dispatches: on a generic operator
// dispatch the key must not exist, so nothing can smuggle it in through params.
func TestWorkflowBlockIDRidesTheStoredPayload(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	execNodeID := uuid.New()

	t.Run("engine dispatch records the block", func(t *testing.T) {
		cmdSvc := &capturingCmdService{}
		s, n := blockIDService(cmdSvc, account)

		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugSetHostname,
			map[string]interface{}{"hostname": "node-01"}, payloadBoundAuth{accountID: account}, &execNodeID, nil, "b2")
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if cmdSvc.registered == nil {
			t.Fatal("no command was registered")
		}
		if got, _ := cmdSvc.registered.Payload[results.WorkflowBlockIDKey].(string); got != "b2" {
			t.Fatalf("payload[%s] = %q, want b2", results.WorkflowBlockIDKey, got)
		}
	})

	t.Run("an operator dispatch cannot smuggle the key through params", func(t *testing.T) {
		cmdSvc := &capturingCmdService{}
		s, n := blockIDService(cmdSvc, account)

		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugSetHostname,
			map[string]interface{}{"hostname": "node-01", results.WorkflowBlockIDKey: "forged"},
			payloadBoundAuth{accountID: account}, nil, nil, "")
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if _, present := cmdSvc.registered.Payload[results.WorkflowBlockIDKey]; present {
			t.Fatal("a caller-supplied block id reached the stored payload; the reconciler would trust a forgery")
		}
	})
}
