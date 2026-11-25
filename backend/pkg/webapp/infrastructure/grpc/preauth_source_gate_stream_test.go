package grpc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// End-to-end shape of the per-source gate through the real EstablishStream: N
// silent streams from source A fill A's cap and A's next stream is refused
// with ResourceExhausted, while a stream from source B arriving at the same
// moment still gets a slot — the property the global gate alone could never
// offer. Then the same scenario with PROXY protocol off, where the per-source
// gate must be inert and only the global gate applies.

func peerCtx(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}})
}

func silentStreamFrom(ip string) *silentStream {
	s := newSilentStream()
	s.ctx = peerCtx(ip)
	return s
}

// releaseSilent lets each parked receiver goroutine exit (see
// preauth_reaper_test for why the test must do this by hand).
func releaseSilent(t *testing.T, streams ...*silentStream) {
	t.Helper()
	for _, s := range streams {
		close(s.released)
		select {
		case <-s.recvReturned:
		case <-time.After(5 * time.Second):
			t.Fatal("a receiver goroutine never came back")
		}
	}
}

func TestPerSourceGateRefusesOneSourceAtItsCapWhileAnotherStillConnects(t *testing.T) {
	restore := preAuthDeadline
	preAuthDeadline = time.Hour // nothing is reaped during this test
	t.Cleanup(func() { preAuthDeadline = restore })

	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)
	srv.preAuth = newPreAuthGate(100) // the global gate is nowhere near binding
	srv.preAuthSource = newPreAuthSourceGate(2)
	srv.preAuthRefusals = newLogThrottle()

	const a, b = "203.0.113.7", "198.51.100.9"

	// Two silent peers from A take A's two slots.
	a1, a2 := silentStreamFrom(a), silentStreamFrom(a)
	go func() { _ = srv.EstablishStream(a1) }()
	go func() { _ = srv.EstablishStream(a2) }()
	waitForInFlight(t, srv, 2)
	if got := srv.preAuthSource.inFlight(a); got != 2 {
		t.Fatalf("per-source in flight for A = %d, want 2", got)
	}

	// A's third stream — an HONEST one this time — is refused before its init
	// is read: the source is at its cap regardless of what it was going to say.
	a3 := &scriptedStream{ctx: peerCtx(a), incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}}
	if err := srv.EstablishStream(a3); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("A's stream past the per-source cap must be refused with ResourceExhausted, got %v", err)
	}
	if svc.connects != 0 {
		t.Fatalf("a refused stream reached the handshake: connects = %d", svc.connects)
	}
	// The refused stream gave its GLOBAL slot back: the acquire order is
	// global then source, and a source refusal must not leak the first.
	waitForInFlight(t, srv, 2)

	// B, at the same moment, is untouched by A's saturation.
	b1 := &scriptedStream{ctx: peerCtx(b), incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}}
	if err := srv.EstablishStream(b1); status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("B was refused while only A is at its cap: %v", err)
	}
	if svc.connects != 1 {
		t.Fatalf("B's honest stream must authenticate, connects = %d", svc.connects)
	}
	// B authenticated and then ended (EOF), so both of its slots are back and
	// its address is gone from the map — the map tracks streams in flight, not
	// addresses seen.
	if got := srv.preAuthSource.inFlight(b); got != 0 {
		t.Fatalf("B still holds %d per-source slot(s) after authenticating", got)
	}
	if got := srv.preAuthSource.sources(); got != 1 {
		t.Fatalf("sources tracked = %d, want 1 (only A, still parked)", got)
	}

	// Let A's silent peers go and prove their slots come back, address and all.
	releaseSilent(t, a1, a2)
	waitForInFlight(t, srv, 0)
	deadline := time.Now().Add(5 * time.Second)
	for srv.preAuthSource.sources() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("A's entry survived its streams ending: sources = %d", srv.preAuthSource.sources())
		}
		time.Sleep(time.Millisecond)
	}
}

// With PROXY protocol off there is NO per-source gate (NewServer leaves it
// nil): every peer is the proxy, so counting per source would refuse the fleet
// at the cap. Only the global gate applies, and three streams from one address
// all park.
func TestPerSourceGateIsInertWithoutProxyProtocol(t *testing.T) {
	restore := preAuthDeadline
	preAuthDeadline = time.Hour
	t.Cleanup(func() { preAuthDeadline = restore })

	srv := testServer(&stubNodeService{})
	srv.preAuth = newPreAuthGate(100)
	if srv.preAuthSource != nil {
		t.Fatal("testServer must not arm the per-source gate; NewServer arms it only behind PROXY protocol")
	}

	const a = "203.0.113.7"
	streams := []*silentStream{silentStreamFrom(a), silentStreamFrom(a), silentStreamFrom(a)}
	for _, s := range streams {
		s := s
		go func() { _ = srv.EstablishStream(s) }()
	}
	// All three park: nothing refused them, and the global count says so.
	waitForInFlight(t, srv, 3)

	releaseSilent(t, streams...)
	waitForInFlight(t, srv, 0)
}

// NewServer arms the per-source gate exactly when the config requires PROXY
// protocol — the wiring rule the design rests on, pinned separately from the
// gate's own behaviour.
func TestNewServerArmsThePerSourceGateOnlyBehindProxyProtocol(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	off := &config.Config{}
	if srv := NewServer(off, discard, &stubNodeService{}); srv.preAuthSource != nil {
		t.Fatal("proxy protocol off must leave the per-source gate nil")
	}
	on := &config.Config{}
	on.GRPC.ProxyProtocol = config.GRPCProxyProtocolRequire
	on.Quota.MaxPreAuthStreamsPerSource = 7
	srv := NewServer(on, discard, &stubNodeService{})
	if srv.preAuthSource == nil {
		t.Fatal("proxy protocol require must arm the per-source gate")
	}
	if got := srv.preAuthSource.limit(); got != 7 {
		t.Fatalf("per-source cap = %d, want the configured 7", got)
	}
}
