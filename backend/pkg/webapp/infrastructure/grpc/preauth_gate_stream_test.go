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

// A full pre-auth gate refuses the NEW stream with ResourceExhausted before the
// handshake is read, and the slot comes back the moment the holder returns —
// so an honest agent's retry (the agent treats ResourceExhausted as recoverable
// and backs off) succeeds once the silent peer is reaped.
//
// Driven through the real EstablishStream with the cap set to ONE, which is
// what the configurable gate is for: before it, proving this took 256 silent
// streams.
func TestAFullPreAuthGateRefusesTheNewStreamAndFreesTheSlotOnReturn(t *testing.T) {
	restore := preAuthDeadline
	preAuthDeadline = 50 * time.Millisecond
	t.Cleanup(func() { preAuthDeadline = restore })

	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)
	srv.preAuth = newPreAuthGate(1)

	// One silent peer takes the only slot.
	silent := newSilentStream()
	silentDone := make(chan error, 1)
	go func() { silentDone <- srv.EstablishStream(silent) }()
	waitForInFlight(t, srv, 1)

	// An honest agent arriving now is refused — immediately, and before its
	// ConnectionInit is read: a refused stream must not reach the handshake.
	honest := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
	}
	if err := srv.EstablishStream(honest); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("a stream past the pre-auth cap must be refused with ResourceExhausted, got %v", err)
	}
	if svc.connects != 0 {
		t.Fatalf("a refused stream reached the handshake: connects = %d", svc.connects)
	}

	// The silent peer is reaped on the deadline and its slot is released.
	select {
	case err := <-silentDone:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("the silent holder must be reaped with DeadlineExceeded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the silent holder was never reaped, so the gate can never drain")
	}
	waitForInFlight(t, srv, 0)

	// The same honest init now authenticates: the cap bounds unauthenticated
	// peers, not the fleet.
	retry := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
	}
	if err := srv.EstablishStream(retry); status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("the slot was not returned when the holder left: %v", err)
	}
	if svc.connects != 1 {
		t.Fatalf("the retried honest stream must authenticate, connects = %d", svc.connects)
	}

	// Let the silent stream's receiver goroutine exit (see preauth_reaper_test).
	close(silent.released)
	select {
	case <-silent.recvReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver goroutine never came back")
	}
}

// waitForInFlight polls the gate until it reports n, since the slot is taken
// on the handler's goroutine and the test cannot observe the acquire directly.
func waitForInFlight(t *testing.T, srv *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for srv.preAuth.inFlight() != n {
		if time.Now().After(deadline) {
			t.Fatalf("pre-auth in flight = %d, want %d", srv.preAuth.inFlight(), n)
		}
		time.Sleep(time.Millisecond)
	}
}
