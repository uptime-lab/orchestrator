package grpc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// holdingService parks every stream it is handed until released — the
// server-side shape of a peer that opens a stream and says nothing. It
// reports each entry so the test knows a stream was actually ADMITTED by the
// transport, which is the property under test.
type holdingService struct {
	proto.UnimplementedNodeCommunicationServer
	entered chan struct{}
	release chan struct{}
}

func (h *holdingService) EstablishStream(stream proto.NodeCommunication_EstablishStreamServer) error {
	h.entered <- struct{}{}
	select {
	case <-h.release:
	case <-stream.Context().Done():
	}
	return nil
}

// The per-connection stream cap is enforced by grpc-go's transport from the
// grpc.MaxConcurrentStreams option, not by anything in this package, so only a
// real transport can show it is applied. This serves the REAL option set over
// an in-memory listener and proves that ONE connection cannot hold a fifth
// silent stream: the (cap+1)th stream is not admitted while the cap is full,
// and is admitted once the held ones return.
//
// Why it matters: the cap used to be 256, equal to the global pre-auth gate,
// so a single credential-less TCP connection could fill the gate and refuse
// every honest reconnect. Filling it now costs one connection per 4 streams,
// which is the dimension the edge's per-IP inFlightConn bounds.
func TestOneConnectionCannotHoldMoreStreamsThanTheCap(t *testing.T) {
	// The value is pinned, not just applied: an honest agent needs one stream
	// and briefly two, and a cap equal to the global pre-auth gate is how one
	// connection filled it. Raising this means re-doing the arithmetic in
	// preauth_limits.go, not editing a number.
	if maxConcurrentStreamsPerConnection != 4 {
		t.Fatalf("maxConcurrentStreamsPerConnection = %d, want 4 (twice the honest ceiling)", maxConcurrentStreamsPerConnection)
	}

	lis := bufconn.Listen(1 << 20)
	srv := &Server{config: &config.Config{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gs := grpc.NewServer(srv.serverOptions()...)
	svc := &holdingService{
		entered: make(chan struct{}, maxConcurrentStreamsPerConnection*4),
		release: make(chan struct{}),
	}
	proto.RegisterNodeCommunicationServer(gs, svc)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := proto.NewNodeCommunicationClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fill the connection: every stream up to the cap is admitted.
	held := make([]proto.NodeCommunication_EstablishStreamClient, 0, maxConcurrentStreamsPerConnection)
	for i := 0; i < maxConcurrentStreamsPerConnection; i++ {
		st, err := client.EstablishStream(ctx)
		if err != nil {
			t.Fatalf("stream %d/%d on one connection must be admitted: %v", i+1, maxConcurrentStreamsPerConnection, err)
		}
		select {
		case <-svc.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("stream %d/%d was never handed to the service", i+1, maxConcurrentStreamsPerConnection)
		}
		held = append(held, st)
	}

	// One more on the SAME connection must not be admitted while the cap is
	// full. grpc-go's client waits for stream quota, so this surfaces as the
	// call's own deadline; either way the service must never see it.
	overCtx, overCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer overCancel()
	over, err := client.EstablishStream(overCtx)
	if err == nil {
		// The client may hand back a stream object before the transport has
		// a slot for it; the first receive is where it would block.
		_, err = over.Recv()
	}
	select {
	case <-svc.entered:
		t.Fatalf("a %dth stream on one connection reached the service: the per-connection cap is not applied",
			maxConcurrentStreamsPerConnection+1)
	default:
	}
	if err == nil {
		t.Fatalf("a %dth stream on one connection must not be admitted while the cap is full",
			maxConcurrentStreamsPerConnection+1)
	}

	// Release the held streams; the next one on the same connection is
	// admitted, so the cap bounds concurrency rather than poisoning the
	// connection.
	close(svc.release)
	for _, st := range held {
		for {
			if _, err := st.Recv(); err != nil {
				break
			}
		}
	}
	afterCtx, afterCancel := context.WithTimeout(ctx, 5*time.Second)
	defer afterCancel()
	if _, err := client.EstablishStream(afterCtx); err != nil {
		t.Fatalf("a stream after the held ones returned must be admitted: %v", err)
	}
	select {
	case <-svc.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream opened after the release never reached the service")
	}
}
