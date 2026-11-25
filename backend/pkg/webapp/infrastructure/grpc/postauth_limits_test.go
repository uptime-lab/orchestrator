package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A node that reconnects while its previous stream is still up must leave
// nothing behind.
//
// The registry assignment was the process's only reference to the previous
// StreamConnection, and every reaper is registry-scoped: the monitor sweeps a
// snapshot of the registry, Server.Stop ranges the map, and handleDisconnection
// deliberately does nothing on a stale teardown. So the displaced connection's
// writer goroutine ran on, holding a buffer nobody would drain, until the
// transport happened to die.
func TestReconnectRetiresDisplacedConnection(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	// The stream the node is about to abandon: registered, with a live writer.
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	displaced := newStreamConnection(oldCtx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: oldCtx}, time.Now(), oldCancel)
	srv.connections[nodeID.String()] = displaced

	// The node reconnects on a fresh stream.
	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
	}
	_ = srv.EstablishStream(stream)

	if !displaced.waitForWriter(2 * time.Second) {
		t.Fatal("the displaced connection's writer is still running; nothing left in the process can reach it")
	}
	select {
	case <-oldCtx.Done():
	default:
		t.Error("the displaced connection was never cancelled, so its handler cannot end until the transport dies")
	}
	if err := displaced.Send(&proto.PlatformToNode{}); err == nil {
		t.Error("a frame enqueued to the displaced connection was accepted; nothing will ever deliver it")
	}
}

// The cap must bind only beyond itself, and must hand the slot back on every
// exit path — a leaked slot would eventually refuse a real node, which is worse
// than the flood the cap exists to bound.
func TestNodeStreamCapRefusesOnlyBeyondTheCap(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)
	srv.nodeStreams = newNodeStreamGate(2)

	// One stream already up for this node: still under the cap.
	release, _, ok := srv.nodeStreams.acquire(nodeID.String())
	if !ok {
		t.Fatal("the first stream was refused under a cap of 2")
	}

	accepted := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
	}
	if err := srv.EstablishStream(accepted); status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("a second stream was refused while under the cap: %v", err)
	}
	if got := srv.nodeStreams.inFlight(nodeID.String()); got != 1 {
		t.Fatalf("the finished stream did not hand its slot back: %d in flight, want 1", got)
	}

	// Fill the cap and try again.
	release2, _, ok := srv.nodeStreams.acquire(nodeID.String())
	if !ok {
		t.Fatal("the second slot was refused under a cap of 2")
	}
	refused := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
	}
	if err := srv.EstablishStream(refused); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("a stream past the cap was accepted: %v", err)
	}

	release()
	release2()
	if got := srv.nodeStreams.inFlight(nodeID.String()); got != 0 {
		t.Fatalf("slots leaked: %d still in flight, want 0", got)
	}

	// Another node is unaffected by this one's ceiling.
	otherID := uuid.New()
	if _, _, ok := srv.nodeStreams.acquire(otherID.String()); !ok {
		t.Error("a different node was refused because of this node's stream count")
	}
}

// Inbound frames that make the platform work are paced; terminal frames are not.
//
// A lower bound on elapsed time, never an upper one: the assertion is that the
// waiting happened at all, which a slow machine cannot make false.
func TestInboundFramesArePacedExceptTerminalOutput(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()

	// 50 frames/second with a burst of one: every frame after the first waits
	// 20 ms, so three heartbeats cost at least 40 ms.
	newSrv := func() *Server {
		svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
		srv := testServer(svc)
		srv.config.Quota.AgentStreamMessagesPerSecond = 50
		srv.config.Quota.AgentStreamBurst = 1
		return srv
	}

	heartbeats := []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}
	for i := 0; i < 3; i++ {
		heartbeats = append(heartbeats, &proto.NodeToPlatform{
			NodeId:    nodeID.String(),
			AccountId: accountID.String(),
			Payload:   &proto.NodeToPlatform_Heartbeat{Heartbeat: &proto.Heartbeat{}},
		})
	}

	start := time.Now()
	_ = newSrv().EstablishStream(&scriptedStream{ctx: context.Background(), incoming: heartbeats})
	paced := time.Since(start)
	if paced < 30*time.Millisecond {
		t.Errorf("a flood of database-touching frames was not paced: %s for 3 frames at 50/s with a burst of 1", paced)
	}

	terminal := []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}
	for i := 0; i < 3; i++ {
		terminal = append(terminal, &proto.NodeToPlatform{
			NodeId:    nodeID.String(),
			AccountId: accountID.String(),
			Payload: &proto.NodeToPlatform_TerminalOutput{
				TerminalOutput: &proto.TerminalOutput{SessionId: uuid.NewString(), Data: []byte("x")},
			},
		})
	}

	start = time.Now()
	_ = newSrv().EstablishStream(&scriptedStream{ctx: context.Background(), incoming: terminal})
	if unpaced := time.Since(start); unpaced >= 30*time.Millisecond {
		t.Errorf("PTY output was paced (%s); an interactive shell would feel broken", unpaced)
	}
}
