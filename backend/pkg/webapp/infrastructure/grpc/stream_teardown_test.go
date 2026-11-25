package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Nothing in the delete path tore a device's stream down, so a
// deleted node kept heartbeating into a platform with no row for it, and a
// RESTORED node stayed offline with an empty agent_version until something else
// broke the connection (a live stream never re-sends ConnectionInit, which is
// the only message that sets those).
//
// These drive the real EstablishStream rather than a helper, because the
// property is about what the message LOOP does — the same reason
// connection_init_once_test.go does.

// heartbeatStubNodeService lets a test choose what UpdateHeartbeat answers.
type heartbeatStubNodeService struct {
	stubNodeService
	heartbeatErr error
	heartbeats   int
}

func (s *heartbeatStubNodeService) UpdateHeartbeat(context.Context, uuid.UUID, time.Time) error {
	s.heartbeats++
	return s.heartbeatErr
}

// connectAgentInfoWrites is how many UpdateAgentInfo calls the ConnectionInit
// path itself makes, before any of these tests' own traffic. Named rather than
// inlined so the assertions below read as "one more than connect" / "no more
// than connect" instead of as bare numbers.
const connectAgentInfoWrites = 1

// commandStubNodeService additionally answers the command-status path.
type commandStubNodeService struct {
	stubNodeService
	agentInfoUpdates int
}

func (s *commandStubNodeService) UpdateAgentInfo(context.Context, uuid.UUID, string, string, []string, time.Time) error {
	s.agentInfoUpdates++
	return nil
}

func (s *commandStubNodeService) RotateAgentInstance(context.Context, uuid.UUID, string) (node.AgentInstanceContinuity, error) {
	return node.AgentInstanceUnknown, nil
}

func (s *commandStubNodeService) MarkAwaitingReturn(context.Context, uuid.UUID, string) error { return nil }

func (s *commandStubNodeService) ClearAwaitingReturn(context.Context, uuid.UUID) error { return nil }

// stubNodeCommandService implements only what the CommandStatusUpdate branch
// calls; the embedded interface is nil so anything else panics loudly.
type stubNodeCommandService struct {
	services.NodeCommandService
	owned *node_command.NodeCommand
	// notApplied makes the update answer "this report changed nothing", which is
	// what an already-terminal command does.
	notApplied bool
}

func (s *stubNodeCommandService) GetNodeCommandByID(context.Context, uuid.UUID, uuid.UUID) (*node_command.NodeCommand, error) {
	return s.owned, nil
}

func (s *stubNodeCommandService) UpdateNodeCommandStatusWithResult(context.Context, uuid.UUID, string, int, string, map[string]string) (bool, error) {
	return !s.notApplied, nil
}

// Also on the connect path, which these tests have to get through first.
func (s *stubNodeCommandService) CompleteRebootCommandsForNode(context.Context, uuid.UUID) error {
	return nil
}

func (s *stubNodeCommandService) FailStalePendingCommandsForNode(context.Context, uuid.UUID, node.AgentInstanceContinuity) error {
	return nil
}

func (s *stubNodeCommandService) RedeliverQueuedCommandsForNode(context.Context, uuid.UUID) error {
	return nil
}

func heartbeat(nodeID, accountID uuid.UUID) *proto.NodeToPlatform {
	return &proto.NodeToPlatform{
		NodeId:    nodeID.String(),
		AccountId: accountID.String(),
		Payload:   &proto.NodeToPlatform_Heartbeat{Heartbeat: &proto.Heartbeat{}},
	}
}

// A heartbeat for a node whose row is gone must END the stream. Until it does,
// the device is perfectly happy and the platform is accepting traffic for
// something it deleted.
func TestHeartbeatForDeletedNodeEndsTheStream(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &heartbeatStubNodeService{
		stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
		heartbeatErr:    app_errors.ErrNodeNotFound,
	}
	srv := testServer(svc)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			heartbeat(nodeID, accountID),
			// Would be processed if the stream survived, which is the bug.
			heartbeat(nodeID, accountID),
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) != codes.NotFound {
		t.Fatalf("a heartbeat for a deleted node must end the stream with NotFound, got %v", err)
	}
	if svc.heartbeats != 1 {
		t.Errorf("the loop kept running after the node was gone: %d heartbeats processed", svc.heartbeats)
	}
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.connections) != 0 {
		t.Errorf("the stream ended but %d connection(s) are still registered", len(srv.connections))
	}
}

// The guard must be keyed on "the row is gone", not on "the write failed". A
// transient database error is an outage, and dropping every agent's stream
// during one turns a database blip into a fleet-wide reconnect storm against a
// platform that is already struggling.
func TestHeartbeatDatabaseErrorKeepsTheStream(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &heartbeatStubNodeService{
		stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
		heartbeatErr:    errors.New("connection refused"),
	}
	srv := testServer(svc)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			heartbeat(nodeID, accountID),
			heartbeat(nodeID, accountID),
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) == codes.NotFound {
		t.Fatalf("a transient heartbeat write failure must not end the stream: %v", err)
	}
	if svc.heartbeats != 2 {
		t.Errorf("expected the loop to survive and process both heartbeats, got %d", svc.heartbeats)
	}
}

// A device that reports it has deleted its own adoption file must not keep the
// stream it opened with that identity. The agent deliberately does not
// disconnect itself — that would race this very report — so the platform closes
// the stream once the report is in.
func TestReleasedIdentityClosesTheStream(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	svc := &commandStubNodeService{
		stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
	}
	srv := testServer(svc)
	srv.nodeCommandService = &stubNodeCommandService{owned: &node_command.NodeCommand{
		ID:     commandID,
		NodeID: nodeID,
		Slug:   node_command.SlugReleaseIdentity,
	}}

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			commandStatus(nodeID, accountID, commandID, string(node_command.StatusSuccess)),
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a successful identity release must close the stream, got %v", err)
	}
	// The status branch's last-seen write must NOT have run: a forced delete has
	// already removed the row it targets, so it would only produce a spurious
	// error line.
	if svc.agentInfoUpdates != connectAgentInfoWrites {
		t.Errorf("the last-seen write ran %d time(s) after the identity was released, expected none beyond connect's %d",
			svc.agentInfoUpdates, connectAgentInfoWrites)
	}
}

// A FAILED release means the device still holds its identity, so the stream
// stays: dropping it would only make the agent reconnect with a credential that
// still works. The heartbeat rule above still ends the stream if the row is gone.
func TestFailedIdentityReleaseKeepsTheStream(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	svc := &commandStubNodeService{
		stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
	}
	srv := testServer(svc)
	srv.nodeCommandService = &stubNodeCommandService{owned: &node_command.NodeCommand{
		ID:     commandID,
		NodeID: nodeID,
		Slug:   node_command.SlugReleaseIdentity,
	}}

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			commandStatus(nodeID, accountID, commandID, string(node_command.StatusFailed)),
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) == codes.Unauthenticated {
		t.Fatalf("a failed release must not close the stream: %v", err)
	}
	if svc.agentInfoUpdates != connectAgentInfoWrites+1 {
		t.Errorf("expected the ordinary last-seen write to run once beyond connect's %d, got %d",
			connectAgentInfoWrites, svc.agentInfoUpdates)
	}
}

// Any other command settling must leave the stream alone — the close is bound to
// release_identity, not to "a command finished".
func TestOtherCommandSuccessKeepsTheStream(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	svc := &commandStubNodeService{
		stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
	}
	srv := testServer(svc)
	srv.nodeCommandService = &stubNodeCommandService{owned: &node_command.NodeCommand{
		ID:     commandID,
		NodeID: nodeID,
		Slug:   node_command.SlugStatusCheck,
	}}

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			commandStatus(nodeID, accountID, commandID, string(node_command.StatusSuccess)),
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) == codes.Unauthenticated {
		t.Fatalf("an ordinary command success closed the stream: %v", err)
	}
	if svc.agentInfoUpdates != connectAgentInfoWrites+1 {
		t.Errorf("expected the ordinary last-seen write to run once beyond connect's %d, got %d",
			connectAgentInfoWrites, svc.agentInfoUpdates)
	}
}

func commandStatus(nodeID, accountID, commandID uuid.UUID, st string) *proto.NodeToPlatform {
	return &proto.NodeToPlatform{
		NodeId:    nodeID.String(),
		AccountId: accountID.String(),
		Payload: &proto.NodeToPlatform_CommandStatusUpdate{
			CommandStatusUpdate: &proto.CommandStatusUpdate{
				CommandId: commandID.String(),
				Status:    st,
				Progress:  100,
			},
		},
	}
}
