package grpc

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"google.golang.org/grpc/peer"
)

// PROXY protocol on the agent listener.
//
// When a reverse proxy forwards :50051 as TCP, every agent connection without
// a PROXY header arrives from the proxy's own address and the gRPC handler's
// `peer.FromContext` cannot tell one client from another (every device reads
// as the proxy's address — see noteAdoptionRejection). With PROXY protocol
// v2 enabled on the proxy's TCP service, each forwarded connection opens
// with a binary header carrying the real client address, and go-proxyproto's
// listener wrapper makes that address what `RemoteAddr()` — and therefore
// `peer.FromContext` — answers.
//
// Two rules the wrapper enforces, both bought by the reasoning in
// config.GRPCProxyProtocolRequire:
//
//   - Policy is REQUIRE, never USE ("optional"). A connection that does not open
//     with a header fails its first read with ErrNoProxyProtocol and grpc-go
//     closes it. Optional would let anyone who reaches a header-less path forge
//     a victim's address into the per-source gate.
//   - The header read is bounded by proxyHeaderReadTimeout. A proxy writes the
//     header before any payload, so a connection that has not produced one in
//     that time is not the proxy's; the pre-auth stream deadline cannot cover
//     this window because no stream exists yet.

// proxyHeaderReadTimeout bounds how long an accepted connection may take to
// produce its PROXY header. A proxy sends it immediately on connect; ten
// seconds is go-proxyproto's own default and far above any real network.
const proxyHeaderReadTimeout = 10 * time.Second

// wrapListenerForProxyProtocol applies the configured PROXY protocol mode to a
// freshly bound listener. `off` returns the listener untouched — no wrapper,
// no per-connection header read — so an install that has not enabled the
// header at its proxy behaves exactly as before. An unrecognised mode is an
// error, never a fallback to `off` (config.GRPCConfig.ProxyProtocolProblem
// carries the reasoning); Start turns that into a refused boot.
//
// Exported to the package's tests through this name so the listener-level test
// wraps a loopback listener exactly the way Start wraps :50051.
func wrapListenerForProxyProtocol(lis net.Listener, grpcCfg config.GRPCConfig) (net.Listener, error) {
	if problem := grpcCfg.ProxyProtocolProblem(); problem != "" {
		return nil, fmt.Errorf("refusing to start the agent listener: %s", problem)
	}
	if !grpcCfg.ProxyProtocolRequired() {
		return lis, nil
	}
	return &proxyproto.Listener{
		Listener: lis,
		// REQUIRE for every upstream, unconditionally. There is no trusted-source
		// allowlist because the only peer that can reach this listener is the
		// proxy on the compose network; a CIDR list here would be a second copy
		// of the compose topology that rots.
		ConnPolicy: func(proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			return proxyproto.REQUIRE, nil
		},
		ReadHeaderTimeout: proxyHeaderReadTimeout,
	}, nil
}

// warnPreAuthSourceRefusal logs one per-source refusal per source per
// logThrottleWindow, carrying how many were suppressed. A source at its cap
// is, by definition, opening streams faster than they are reaped, so an
// unthrottled line here would reproduce in the log the flood it reports on —
// the same reasoning as the terminal-frame and adoption-rejection throttles.
// The key is a transport-derived IP, bounded by construction, never a
// peer-supplied string.
func (s *Server) warnPreAuthSourceRefusal(source string) {
	suppressed := 0
	if s.preAuthRefusals != nil {
		allow, n := s.preAuthRefusals.allow(source, time.Now())
		if !allow {
			return
		}
		suppressed = n
	}
	s.logger.Warn("refusing gRPC stream: too many unauthenticated streams from one source",
		"source_ip", source,
		"in_flight_from_source", s.preAuthSource.inFlight(source),
		"cap_per_source", s.preAuthSource.limit(),
		"in_flight_total", s.preAuth.inFlight(),
		"suppressed_since_last_line", suppressed,
	)
}

// peerSourceKey reduces the stream's peer address to the key the per-source
// pre-auth gate counts on: the bare IP, port stripped, an IPv4 address mapped
// into IPv6 (`::ffff:203.0.113.7`) folded back to its IPv4 spelling.
//
// The port is stripped because a retrying client gets a fresh source port every
// attempt, so counting host:port would bound nothing. The v4-in-v6 fold matters
// because Go's dual-stack listener reports a v4 client either way depending on
// how the socket was bound, and two spellings of one address would be two
// buckets — twice the cap for whoever knows to alternate.
//
// ok is false when the transport exposes no peer at all (a hand-built stream in
// a test, or a transport with no address). The caller then applies the global
// gate alone rather than inventing a shared bucket every such peer would land
// in. A peer whose address is not host:port (a Unix socket) is keyed on its
// bounded string, since it is still a distinct source.
func peerSourceKey(ctx context.Context) (key string, ok bool) {
	p, found := peer.FromContext(ctx)
	if !found || p.Addr == nil {
		return "", false
	}
	return sourceKeyFromAddr(p.Addr), true
}

// sourceKeyFromAddr is peerSourceKey's address half, separated so the
// canonicalisation can be tested without a context.
func sourceKeyFromAddr(addr net.Addr) string {
	if ap, err := netip.ParseAddrPort(addr.String()); err == nil {
		return ap.Addr().Unmap().WithZone("").String()
	}
	if ip, err := netip.ParseAddr(addr.String()); err == nil {
		return ip.Unmap().WithZone("").String()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return boundedIdentifier(addr.String())
	}
	return boundedIdentifier(host)
}
