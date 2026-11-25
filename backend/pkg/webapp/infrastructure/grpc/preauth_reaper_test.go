package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// silentStream is the peer this whole mechanism exists for: it opens a stream,
// authenticates nothing, and never sends a frame. Recv blocks until the test
// releases it, exactly as a real one blocks until the transport dies.
type silentStream struct {
	grpc.ServerStream
	ctx      context.Context
	released chan struct{}
	// recvReturned closes when the blocked Recv has actually come back, which is
	// how the test proves the receiver goroutine is not leaked.
	recvReturned chan struct{}
}

func newSilentStream() *silentStream {
	return &silentStream{
		ctx:          context.Background(),
		released:     make(chan struct{}),
		recvReturned: make(chan struct{}),
	}
}

func (s *silentStream) Recv() (*proto.NodeToPlatform, error) {
	<-s.released
	close(s.recvReturned)
	return nil, context.Canceled
}

func (s *silentStream) Send(*proto.PlatformToNode) error { return nil }

func (s *silentStream) Context() context.Context { return s.ctx }

// A peer that opens a stream and says nothing must be hung up on, and its
// pre-auth slot must come back.
//
// Cancelling a context derived from stream.Context() is not enough: grpc-go's
// Recv does not observe it, so the handler would park inside Recv, the message
// loop would never reach its ctx.Done() check, and releasePreAuth — a deferred
// call in a function that never returns — would never run. Silent streams would
// then hold the pre-auth cap with no credentials.
//
// The property is "the HANDLER returns", not "a context was cancelled":
// returning is the only thing that ends a gRPC stream from the server side, and
// a test that asserted on the cancellation would pass while the handler stays
// parked.
func TestSilentPeerIsReapedAndReleasesItsPreAuthSlot(t *testing.T) {
	restore := preAuthDeadline
	preAuthDeadline = 50 * time.Millisecond
	t.Cleanup(func() { preAuthDeadline = restore })

	srv := testServer(&stubNodeService{})
	stream := newSilentStream()

	done := make(chan error, 1)
	go func() { done <- srv.EstablishStream(stream) }()

	select {
	case err := <-done:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("a reaped pre-auth stream must end with DeadlineExceeded so the peer is told why, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EstablishStream never returned for a peer that sent nothing — the reaper cannot end a stream by cancelling a context")
	}

	if got := srv.preAuth.inFlight(); got != 0 {
		t.Fatalf("pre-auth slots still held after the stream was reaped: %d; the cap is exhausted permanently at this rate", got)
	}

	// The receiver goroutine outlives the handler by design — it is blocked in
	// Recv, and only the stream ending unblocks it. Releasing it here stands in
	// for that, and proves it then exits rather than parking on a channel send
	// nobody is reading.
	close(stream.released)
	select {
	case <-stream.recvReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver goroutine never came back")
	}
}

// The reaper must not touch a stream that authenticated in time: it fires on a
// timer regardless, and an armed cancel that ignores `authed` would hang up on
// every real agent 15 seconds into its session.
func TestAuthenticatedStreamSurvivesThePreAuthDeadline(t *testing.T) {
	restore := preAuthDeadline
	preAuthDeadline = 20 * time.Millisecond
	t.Cleanup(func() { preAuthDeadline = restore })

	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	stream := &pausedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)},
		hold:     make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() { done <- srv.EstablishStream(stream) }()

	// Well past the deadline: an authenticated stream must still be there.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("an authenticated stream was closed by the pre-auth reaper: %v", err)
	default:
	}
	close(stream.hold)
	<-done
}

// pausedStream replays its script and then blocks, so the test can observe a
// stream that is alive rather than one that has already ended on EOF.
type pausedStream struct {
	grpc.ServerStream
	ctx      context.Context
	incoming []*proto.NodeToPlatform
	hold     chan struct{}
}

func (s *pausedStream) Recv() (*proto.NodeToPlatform, error) {
	if len(s.incoming) > 0 {
		msg := s.incoming[0]
		s.incoming = s.incoming[1:]
		return msg, nil
	}
	<-s.hold
	return nil, context.Canceled
}

func (s *pausedStream) Send(*proto.PlatformToNode) error { return nil }

func (s *pausedStream) Context() context.Context { return s.ctx }
