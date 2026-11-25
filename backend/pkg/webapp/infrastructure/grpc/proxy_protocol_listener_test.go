package grpc

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// The listener-level contract: wrapped the way Start wraps :50051, a
// connection that opens with a PROXY v2 header makes peer.FromContext inside
// the gRPC handler answer the HEADER's source address — the client's, not the
// proxy's — and under `require` a connection without one never reaches a
// handler at all.
//
// Driven through a real grpc.Server on a loopback listener rather than a
// hand-built stream, because the property under test is what the TRANSPORT
// reports, which a fake stream context cannot exercise.

// peerEchoServer answers every RPC with a status whose message is the peer
// address the handler observed, so the test reads the transport's view of the
// client through the ordinary error channel and needs no service definition.
func peerEchoServer(t *testing.T, lis net.Listener) *grpc.Server {
	t.Helper()
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ interface{}, stream grpc.ServerStream) error {
		p, ok := peer.FromContext(stream.Context())
		if !ok || p.Addr == nil {
			return status.Error(codes.Internal, "no peer on context")
		}
		return status.Error(codes.Aborted, "peer="+p.Addr.String())
	}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return srv
}

// proxyHeaderDialer dials the listener and writes a PROXY v2 header naming
// source as the client, exactly as Traefik does with proxyProtocol.version: 2
// on the TCP service, before handing the connection to grpc-go.
func proxyHeaderDialer(source *net.TCPAddr) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		dest := conn.RemoteAddr()
		hdr := proxyproto.HeaderProxyFromAddrs(2, source, dest)
		if _, err := hdr.WriteTo(conn); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// observedPeer opens one RPC and returns the peer address the server saw, or
// the error the client got when no handler ran.
func observedPeer(t *testing.T, addr string, dialer func(context.Context, string) (net.Conn, error)) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if dialer != nil {
		opts = append(opts, grpc.WithContextDialer(dialer))
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	err = conn.Invoke(ctx, "/peer.Echo/Get", &proto.NodeToPlatform{}, &proto.NodeToPlatform{})
	if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted && strings.HasPrefix(st.Message(), "peer=") {
		return strings.TrimPrefix(st.Message(), "peer="), nil
	}
	return "", err
}

func requireProxyProtocol() config.GRPCConfig {
	return config.GRPCConfig{ProxyProtocol: config.GRPCProxyProtocolRequire}
}

func TestRequiredProxyProtocolMakesTheHandlerSeeTheHeadersSourceAddress(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	lis, err := wrapListenerForProxyProtocol(raw, requireProxyProtocol())
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, isProxy := lis.(*proxyproto.Listener); !isProxy {
		t.Fatalf("require must install the proxyproto listener, got %T", lis)
	}
	peerEchoServer(t, lis)

	client := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40123}
	got, err := observedPeer(t, raw.Addr().String(), proxyHeaderDialer(client))
	if err != nil {
		t.Fatalf("an RPC behind a PROXY header must reach the handler, got %v", err)
	}
	if got != client.String() {
		t.Fatalf("handler saw peer %q, want the PROXY header's source %q (the proxy's own address would be 127.0.0.1)", got, client)
	}
}

func TestRequiredProxyProtocolRefusesAConnectionWithoutAHeader(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	lis, err := wrapListenerForProxyProtocol(raw, requireProxyProtocol())
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	peerEchoServer(t, lis)

	got, err := observedPeer(t, raw.Addr().String(), nil)
	if err == nil {
		t.Fatalf("a header-less connection reached the handler (peer %q); REQUIRE must refuse it before any RPC runs", got)
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
		t.Fatalf("the handler ran for a header-less connection: %v", err)
	}
}

func TestProxyProtocolOffLeavesTheListenerUntouched(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	for _, mode := range []string{"", config.GRPCProxyProtocolOff} {
		lis, err := wrapListenerForProxyProtocol(raw, config.GRPCConfig{ProxyProtocol: mode})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if lis != raw {
			t.Fatalf("mode %q must return the raw listener, got %T", mode, lis)
		}
	}
	peerEchoServer(t, raw)

	// Header-less works, and the peer is the real TCP peer (loopback).
	got, err := observedPeer(t, raw.Addr().String(), nil)
	if err != nil {
		t.Fatalf("plain connection under off: %v", err)
	}
	host, _, _ := net.SplitHostPort(got)
	if host != "127.0.0.1" {
		t.Fatalf("under off the handler must see the TCP peer, got %q", got)
	}
}

func TestAnUnrecognisedProxyProtocolModeRefusesToStart(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer raw.Close()
	for _, bad := range []string{"required", "optional", "on", "v2", "true"} {
		lis, err := wrapListenerForProxyProtocol(raw, config.GRPCConfig{ProxyProtocol: bad})
		if err == nil {
			t.Fatalf("mode %q was accepted (as %T); an unrecognised mode must refuse, never fall back to off", bad, lis)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Fatalf("the refusal for %q must name the value: %v", bad, err)
		}
	}
	// And the sentinel that makes the refusal specific is go-proxyproto's own
	// policy error surface, not a wrapper of ours — pin that the package still
	// exposes it, since the require test above relies on its behaviour.
	if !errors.Is(proxyproto.ErrNoProxyProtocol, proxyproto.ErrNoProxyProtocol) {
		t.Fatal("unreachable")
	}
}
