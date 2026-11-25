package grpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStream is a BidiStreamingServer that records Sends and can be told to
// block or fail. It also DETECTS concurrent Send calls, which is the whole point
// of the writer: grpc-go's real stream does not detect it, it corrupts.
type fakeStream struct {
	grpc.BidiStreamingServer[proto.NodeToPlatform, proto.PlatformToNode]

	mu       sync.Mutex
	sent     []*proto.PlatformToNode
	inFlight int
	overlaps int

	block   chan struct{} // when non-nil, every Send waits on it
	failNow error
}

func (f *fakeStream) Send(msg *proto.PlatformToNode) error {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > 1 {
		f.overlaps++
	}
	block, failNow := f.block, f.failNow
	f.mu.Unlock()

	if block != nil {
		<-block
	}

	f.mu.Lock()
	f.inFlight--
	if failNow == nil {
		f.sent = append(f.sent, msg)
	}
	f.mu.Unlock()
	return failNow
}

func (f *fakeStream) Context() context.Context { return context.Background() }
func (f *fakeStream) SetHeader(metadata.MD) error {
	return nil
}

func (f *fakeStream) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeStream) overlapCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlaps
}

func testConnection(t *testing.T, stream *fakeStream) (*StreamConnection, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn := newStreamConnection(ctx, quietLogger(), "node-1", "account-1", nil, stream, time.Now(), cancel)
	t.Cleanup(func() {
		conn.Stop()
		cancel()
	})
	return conn, cancel
}

func frame() *proto.PlatformToNode {
	return &proto.PlatformToNode{
		Payload: &proto.PlatformToNode_Command{Command: &proto.Command{CommandId: "c1"}},
	}
}

func TestFramesReachTheStream(t *testing.T) {
	stream := &fakeStream{}
	conn, _ := testConnection(t, stream)

	for i := 0; i < 5; i++ {
		if err := conn.Send(frame()); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for stream.sentCount() < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := stream.sentCount(); got != 5 {
		t.Fatalf("the stream received %d frames, want 5", got)
	}
}

// The command dispatcher and the terminal handlers must never both write to one
// gRPC stream. grpc-go forbids that and does not detect it — it corrupts the
// connection or kills the process. Here the fake stream detects it, so the test
// fails loudly if a second writer ever reappears.
func TestConcurrentSendersNeverOverlapOnTheStream(t *testing.T) {
	stream := &fakeStream{}
	conn, _ := testConnection(t, stream)

	const senders, each = 8, 25
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if err := conn.Send(frame()); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for stream.sentCount() < senders*each && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := stream.sentCount(); got != senders*each {
		t.Fatalf("the stream received %d frames, want %d", got, senders*each)
	}
	if overlaps := stream.overlapCount(); overlaps != 0 {
		t.Fatalf("%d concurrent Send calls reached one stream — grpc-go forbids this", overlaps)
	}
}

// Fleet-wide dispatch runs in ONE goroutine, so a node that has stopped
// reading its stream must not hold that goroutine. Enqueueing to a wedged node
// has to return, and quickly.
func TestAWedgedNodeDoesNotHoldTheCaller(t *testing.T) {
	stream := &fakeStream{block: make(chan struct{})}
	conn, _ := testConnection(t, stream)
	defer close(stream.block)

	// Fill the buffer plus the one frame the blocked writer is holding.
	for i := 0; i < outboundBuffer+1; i++ {
		if err := conn.Send(frame()); err != nil {
			t.Fatalf("filling the buffer failed at %d: %v", i, err)
		}
	}

	start := time.Now()
	err := conn.Send(frame())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("enqueueing to a node that is not reading its stream reported success")
	}
	if elapsed > 3*enqueueGrace {
		t.Fatalf("the enqueue took %s; one wedged node would stall the whole dispatch loop", elapsed)
	}
	// And the buffered frames are still intact, waiting for the node.
	if stream.sentCount() != 0 {
		t.Fatalf("the blocked stream reported %d sends", stream.sentCount())
	}
}

// Nothing that happens to one node's stream may affect another's.
func TestOneWedgedNodeDoesNotDelayAnother(t *testing.T) {
	wedged := &fakeStream{block: make(chan struct{})}
	healthy := &fakeStream{}
	wedgedConn, _ := testConnection(t, wedged)
	healthyConn, _ := testConnection(t, healthy)
	defer close(wedged.block)

	for i := 0; i < outboundBuffer+1; i++ {
		_ = wedgedConn.Send(frame())
	}

	start := time.Now()
	if err := healthyConn.Send(frame()); err != nil {
		t.Fatalf("the healthy node's send failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > enqueueGrace {
		t.Fatalf("sending to a healthy node took %s while another node was wedged", elapsed)
	}
}

// A momentary hiccup must not fail a command: the enqueue waits out the grace and
// succeeds as soon as the writer drains one frame.
func TestASlowNodeIsNotFailedIfItCatchesUp(t *testing.T) {
	stream := &fakeStream{block: make(chan struct{})}
	conn, _ := testConnection(t, stream)

	for i := 0; i < outboundBuffer+1; i++ {
		if err := conn.Send(frame()); err != nil {
			t.Fatalf("filling the buffer failed at %d: %v", i, err)
		}
	}

	// Unblock the writer shortly before the grace expires.
	go func() {
		time.Sleep(enqueueGrace / 4)
		close(stream.block)
	}()

	if err := conn.Send(frame()); err != nil {
		t.Fatalf("a node that caught up within the grace was failed anyway: %v", err)
	}
}

// Once the stream is finished, every later enqueue must fail with the reason
// rather than park a frame in a buffer nobody will drain.
func TestSendsAfterAStreamErrorFailWithTheReason(t *testing.T) {
	stream := &fakeStream{failNow: errors.New("transport is closing")}
	conn, _ := testConnection(t, stream)

	// The first frame reaches the writer, whose Send fails and stops it.
	if err := conn.Send(frame()); err != nil {
		t.Fatalf("the first enqueue should succeed; it is the writer that fails: %v", err)
	}
	if !conn.waitForWriter(2 * time.Second) {
		t.Fatal("the writer did not stop after a stream error")
	}

	err := conn.Send(frame())
	if err == nil {
		t.Fatal("a send after the stream failed reported success")
	}
	if !strings.Contains(err.Error(), "transport is closing") {
		t.Fatalf("the error does not carry the reason the writer stopped: %v", err)
	}
}

func TestStopEndsTheWriterAndIsIdempotent(t *testing.T) {
	stream := &fakeStream{}
	conn, _ := testConnection(t, stream)

	conn.Stop()
	conn.Stop() // must not panic on a second close

	if !conn.waitForWriter(2 * time.Second) {
		t.Fatal("the writer did not stop")
	}
	if err := conn.Send(frame()); err == nil {
		t.Fatal("a send after Stop reported success")
	}
}

// The stream context is the other way a writer ends: the monitor cancels it when
// it declares a connection stale.
func TestCancellingTheStreamContextEndsTheWriter(t *testing.T) {
	stream := &fakeStream{}
	conn, cancel := testConnection(t, stream)

	cancel()

	if !conn.waitForWriter(2 * time.Second) {
		t.Fatal("the writer did not stop when the stream context was cancelled")
	}
}

// A connection built as a bare struct literal (which tests do) has no writer.
// Send must report that rather than block forever on a nil channel.
func TestAConnectionWithNoWriterFailsInsteadOfBlocking(t *testing.T) {
	conn := &StreamConnection{NodeID: "node-1"}

	done := make(chan error, 1)
	go func() { done <- conn.Send(frame()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a connection with no writer reported a successful send")
		}
	case <-time.After(time.Second):
		t.Fatal("Send blocked on a connection with no writer")
	}
}

func TestSendRefusesANilFrame(t *testing.T) {
	stream := &fakeStream{}
	conn, _ := testConnection(t, stream)

	if err := conn.Send(nil); err == nil {
		t.Fatal("a nil frame was accepted")
	}
}
