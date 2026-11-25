package grpc

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubNodeService implements only what the ConnectionInit path calls. The
// embedded interface is nil, so anything else panics loudly rather than
// silently returning a zero value — the same idiom as the auth-service stubs.
type stubNodeService struct {
	services.NodeService
	connected  *node.Node
	connects   int
	connectErr error

	// The master-token arm: what ConnectWithMasterToken answers, and how often
	// it was asked. The service owns the refusal decision;
	// these tests pin what the HANDLER does with each answer.
	masterConnected *node.Node
	masterErr       error
	masterConnects  int

	// lookup, when set, answers GetByIDAndAccount instead of `connected` —
	// the scope-refresh tests use it as the "committed row" the live OU scope
	// must converge on.
	lookup func(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error)
}

func (s *stubNodeService) ConnectWithMasterToken(context.Context, uuid.UUID, uuid.UUID, node.HardwareFingerprint, services.NodeDefaults) (*node.Node, error) {
	s.masterConnects++
	return s.masterConnected, s.masterErr
}

// stubMasterTokenService accepts any token value as this account's.
type stubMasterTokenService struct {
	accountID uuid.UUID
}

func (s *stubMasterTokenService) ValidateToken(context.Context, string) (*master_token.MasterToken, error) {
	return &master_token.MasterToken{ID: uuid.New(), AccountID: s.accountID}, nil
}

// RecordIdentityReleased is reached on a release_identity success, which the
// teardown tests drive; the embedded nil interface would panic instead.
func (s *stubNodeService) RecordIdentityReleased(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *stubNodeService) NoteDeletedNodeRefused(context.Context, uuid.UUID, uuid.UUID, string, string) {
}

func (s *stubNodeService) Connect(context.Context, node_connect.NodeConnectCommand) (*node.Node, error) {
	s.connects++
	if s.connectErr != nil {
		return nil, s.connectErr
	}
	return s.connected, nil
}

func (s *stubNodeService) UpdateAgentInfo(context.Context, uuid.UUID, string, string, []string, time.Time) error {
	return nil
}

func (s *stubNodeService) RotateAgentInstance(context.Context, uuid.UUID, string) (node.AgentInstanceContinuity, error) {
	return node.AgentInstanceUnknown, nil
}

func (s *stubNodeService) MarkAwaitingReturn(context.Context, uuid.UUID, string) error { return nil }

func (s *stubNodeService) ClearAwaitingReturn(context.Context, uuid.UUID) error { return nil }

func (s *stubNodeService) UpdateStatus(context.Context, uuid.UUID, string) error { return nil }

func (s *stubNodeService) UpdateHeartbeat(context.Context, uuid.UUID, time.Time) error { return nil }

// GetByID feeds the node_updated push that updateNodeStatus makes once a
// websocket client is wired; it answers the same row as the connect.
func (s *stubNodeService) GetByID(context.Context, node_get.NodeGetQuery) (*node.Node, error) {
	return s.connected, nil
}

func (s *stubNodeService) GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	if s.lookup != nil {
		return s.lookup(ctx, nodeID, accountID)
	}
	return s.connected, nil
}

// scriptedStream replays a fixed list of client messages and then blocks the
// way a real stream does when the peer is idle — here, by reporting EOF.
type scriptedStream struct {
	grpc.ServerStream
	ctx      context.Context
	incoming []*proto.NodeToPlatform
	sent     []*proto.PlatformToNode
}

func (s *scriptedStream) Recv() (*proto.NodeToPlatform, error) {
	if len(s.incoming) == 0 {
		return nil, io.EOF
	}
	msg := s.incoming[0]
	s.incoming = s.incoming[1:]
	return msg, nil
}

func (s *scriptedStream) Send(m *proto.PlatformToNode) error {
	s.sent = append(s.sent, m)
	return nil
}

func (s *scriptedStream) Context() context.Context { return s.ctx }

func connectionInit(nodeID, accountID uuid.UUID) *proto.NodeToPlatform {
	return &proto.NodeToPlatform{
		NodeId:      nodeID.String(),
		AccountId:   accountID.String(),
		UniqueToken: "a-node-token",
		Payload: &proto.NodeToPlatform_Connection{
			Connection: &proto.ConnectionInit{
				Fingerprint: &proto.HardwareFingerprint{
					MacAddress:     "2c:cf:67:12:34:56",
					HardwareSerial: "10000000abcd0001",
				},
				AgentVersion: "0.4.8",
			},
		},
	}
}

// A second ConnectionInit on an established stream must be REFUSED.
//
// Without the guard, the branch would run again from the top: it would build a
// second StreamConnection over the same gRPC stream — and therefore a second
// writer goroutine — then overwrite the map entry so the first is never
// retired. Two goroutines calling SendMsg on one stream is precisely what
// stream_writer.go exists to prevent; grpc-go forbids it, does not detect it,
// and it can kill the process.
//
// The test drives the real EstablishStream rather than asserting on a helper,
// because the property is about what the LOOP does with a second message.
func TestSecondConnectionInitIsRefused(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			connectionInit(nodeID, accountID), // the one that must be refused
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a second ConnectionInit must be refused with FailedPrecondition, got %v", err)
	}
	// The decisive assertion: the second init must not have re-entered the
	// branch. Exactly one connect means exactly one StreamConnection, and
	// therefore exactly one writer goroutine, over this stream.
	if svc.connects != 1 {
		t.Errorf("the connect path ran %d times; the second init re-entered it and built another writer goroutine", svc.connects)
	}
	// And nothing is left registered once the stream ends. Deliberately NOT an
	// assertion that one connection SURVIVES: the deferred handleDisconnection
	// retires it, which is correct. What would be wrong is an orphan — the old
	// bug overwrote the map entry, so the first connection's writer outlived
	// every reference to it.
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.connections) != 0 {
		t.Errorf("the stream ended but %d connection(s) are still registered — an orphaned writer", len(srv.connections))
	}
}

// The honest agent's shape must keep working: one init, then ordinary traffic,
// then the peer goes away. Nothing here may be refused.
func TestSingleConnectionInitStillWorks(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			{
				NodeId:    nodeID.String(),
				AccountId: accountID.String(),
				Payload:   &proto.NodeToPlatform_Heartbeat{Heartbeat: &proto.Heartbeat{}},
			},
		},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) == codes.FailedPrecondition {
		t.Fatalf("an ordinary agent session was refused: %v", err)
	}
	if svc.connects != 1 {
		t.Errorf("expected exactly one connect, got %d", svc.connects)
	}
}

// masterTokenInit is the shape the agent sends when it holds no identity of
// its own: the nil UUID as node id, the master token in the token field.
func masterTokenInit(accountID uuid.UUID) *proto.NodeToPlatform {
	msg := connectionInit(uuid.Nil, accountID)
	msg.UniqueToken = "a-master-token"
	return msg
}

func identityAssignments(sent []*proto.PlatformToNode) []*proto.IdentityAssignment {
	var out []*proto.IdentityAssignment
	for _, m := range sent {
		if ia := m.GetIdentityAssignment(); ia != nil {
			out = append(out, ia)
		}
	}
	return out
}

// A master-token handshake the service REFUSES (it named an admitted node it
// could not prove to be) must leave that node alone at the
// handler too: no identity leaves the platform, and the node's live stream —
// which is one of the two reasons the service refuses — stays registered
// untouched, so the real device is neither displaced nor evicted.
func TestRefusedMasterTokenInitLeavesTheNodeAndItsStreamAlone(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{masterErr: app_errors.ErrNodeNotFound}
	srv := testServer(svc)
	srv.SetMasterTokenService(&stubMasterTokenService{accountID: accountID})

	// The real device's stream, registered before the impostor arrives.
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	live := newStreamConnection(liveCtx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: liveCtx}, time.Now(), liveCancel)
	srv.mu.Lock()
	srv.connections[nodeID.String()] = live
	srv.mu.Unlock()

	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{masterTokenInit(accountID)},
	}

	err := srv.EstablishStream(stream)

	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a refused master-token init must answer Unauthenticated, got %v", err)
	}
	if svc.masterConnects != 1 {
		t.Errorf("ConnectWithMasterToken ran %d times, want 1", svc.masterConnects)
	}
	if got := identityAssignments(stream.sent); len(got) != 0 {
		t.Errorf("a refused handshake was sent %d IdentityAssignment(s): %+v", len(got), got)
	}
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if srv.connections[nodeID.String()] != live {
		t.Errorf("the real device's registered stream was displaced or removed by a refused handshake")
	}
	if liveCtx.Err() != nil {
		t.Errorf("the real device's stream context was cancelled by a refused handshake")
	}
}

// The reimage-from-master-SD flow: the service answers with the OFFLINE
// admitted node the device already is, and the handler restores its identity.
// This is the "expired master token stranded a node" recovery and must keep
// working after the refusal above exists.
func TestMasterTokenInitHandsIdentityToOfflineAdmittedNode(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	admitted := &node.Node{
		ID:             nodeID,
		AccountID:      accountID,
		UniqueToken:    "n-token",
		ApprovalStatus: node.ApprovalStatusApproved,
	}
	svc := &stubNodeService{masterConnected: admitted, connected: admitted}
	srv := testServer(svc)
	srv.SetMasterTokenService(&stubMasterTokenService{accountID: accountID})

	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{masterTokenInit(accountID)},
	}

	if err := srv.EstablishStream(stream); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("the device that is the node was refused: %v", err)
	}
	got := identityAssignments(stream.sent)
	if len(got) != 1 {
		t.Fatalf("want exactly one IdentityAssignment, got %d", len(got))
	}
	if got[0].GetNodeId() != nodeID.String() || got[0].GetUniqueToken() != "n-token" {
		t.Errorf("IdentityAssignment = %+v, want node %s with its own token", got[0], nodeID)
	}
}

// A node awaiting approval is handed no identity: the operator's Reject must
// still be able to end it, and node-token Connect checks no approval.
func TestMasterTokenInitWithholdsIdentityFromPendingNode(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	pending := &node.Node{
		ID:             nodeID,
		AccountID:      accountID,
		UniqueToken:    "n-token",
		ApprovalStatus: node.ApprovalStatusPendingApproval,
	}
	svc := &stubNodeService{masterConnected: pending, connected: pending}
	srv := testServer(svc)
	srv.SetMasterTokenService(&stubMasterTokenService{accountID: accountID})

	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{masterTokenInit(accountID)},
	}

	if err := srv.EstablishStream(stream); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("a pending node's device was refused: %v", err)
	}
	if got := identityAssignments(stream.sent); len(got) != 0 {
		t.Errorf("a pending node was sent %d IdentityAssignment(s)", len(got))
	}
}

func testServer(svc services.NodeService) *Server {
	return &Server{
		config:             &config.Config{},
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		nodeService:        svc,
		connections:        map[string]*StreamConnection{},
		adoptionRejections: newAdoptionRejectionTracker(),
		preAuth:            newPreAuthGate(0),
	}
}
