package workflow_execution

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// blockedByDefault CIDRs that are not covered by the net.IP helpers below but
// must never be reachable from a tenant-controlled SSRF sink (CGNAT, IETF
// benchmarking/protocol assignment, "this host" 0.0.0.0/8).
var blockedByDefault = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "0.0.0.0/8"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isBlockedIP reports whether an IP is in a range a server-side request must
// never reach: loopback, private (RFC1918 + IPv6 ULA), link-local (which
// includes the 169.254.169.254 cloud metadata endpoint), multicast,
// unspecified, and the extra ranges above.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() {
		return true
	}
	for _, n := range blockedByDefault {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// newSafeHTTPClient returns an http.Client for tenant-controlled outbound
// requests (the workflow http_request block). It defends against SSRF:
//   - the DialContext resolves the host and rejects any blocked IP, then dials
//     that exact resolved IP — so DNS rebinding (resolve-public, dial-private)
//     cannot slip past a name-only check;
//   - redirects are NOT followed (a tenant 302 -> internal is returned as the
//     redirect response, never fetched).
// Callers must still restrict the scheme to http/https before use.
// httpClientFor is the seam a test replaces to drive execHTTPRequest end to
// end. It cannot be done any other way: the guard below refuses an internal
// address at DIAL time — which is the whole point of it — so an httptest server
// on 127.0.0.1 is refused before any response exists. Package-private and
// assigned nowhere in production.
var httpClientFor = newSafeHTTPClient

// The transport is ONE per process, not one per block. A
// per-block http.Transport kept the keep-alive connection its response came
// back on: nothing ever closed it, because the transport that owned it was
// dropped on the floor with the connection still parked in its idle pool —
// neither the request context nor http.Client.Timeout retires an idle
// keep-alive — so every completed http_request block leaked one descriptor
// and one goroutine for as long as the peer kept the socket open. Sharing one
// bounded pool makes the idle set a steady state instead of a sum.
const (
	// safeHTTPMaxIdleConns bounds the idle pool across every destination.
	safeHTTPMaxIdleConns = 16
	// safeHTTPMaxIdleConnsPerHost bounds it per destination, so one busy
	// playbook target cannot own the whole pool.
	safeHTTPMaxIdleConnsPerHost = 2
	// safeHTTPIdleConnTimeout retires an idle connection nobody reused.
	safeHTTPIdleConnTimeout = 30 * time.Second
)

var (
	sharedSafeTransportOnce sync.Once
	sharedSafeTransport     *http.Transport
)

// safeDial resolves the host, refuses any blocked address, and dials the exact
// IP it validated. It is the SSRF guard, and it lives on the shared transport,
// so every request through the pool is dialed by it.
func safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if isBlockedIP(ip.IP) {
			return nil, fmt.Errorf("refusing to connect to blocked address %s", ip.IP)
		}
	}
	// Dial the exact IP we validated (avoids a second, rebindable lookup).
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// newSafeTransport builds the bounded transport around a dial function. The
// dial is a parameter only so a test can prove the pool bounds against a
// loopback server, which safeDial refuses by design; production passes
// safeDial and nothing else.
func newSafeTransport(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         dial,
		TLSHandshakeTimeout: 10 * time.Second,
		// The per-request bound is the request context's deadline and the
		// client's Timeout, both set per block; this is the ceiling those are
		// clamped to, so a shared transport needs no per-call value.
		ResponseHeaderTimeout: maxHTTPRequestTimeoutSeconds * time.Second,
		MaxIdleConns:          safeHTTPMaxIdleConns,
		MaxIdleConnsPerHost:   safeHTTPMaxIdleConnsPerHost,
		IdleConnTimeout:       safeHTTPIdleConnTimeout,
	}
}

func safeTransport() *http.Transport {
	sharedSafeTransportOnce.Do(func() {
		sharedSafeTransport = newSafeTransport(safeDial)
	})
	return sharedSafeTransport
}

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: safeTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// safeURLError formats a transport error for storage without the request URL's
// query string.
//
// Go's *url.Error embeds the whole URL it was given, so `{{secrets.token}}`
// resolved into a query parameter used to be copied verbatim into
// workflow_execution_nodes.graph_state — a plaintext jsonb column on no rewrap
// list, so the value outlives a SECRETS_ENCRYPTION_KEY rotation as
// unreadable-but-present material. The host and path stay: this text is what an
// operator triages a failed block with.
func safeURLError(prefix string, err error, rawURL string) string {
	msg := err.Error()
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, node_command_domain.RedactURLCredentialsInDetails(rawURL))
	}
	// Belt and braces: the error may quote a URL we never handed it (a redirect
	// target, a proxy URL), so run the whole message through the same redactor.
	return fmt.Sprintf("%s: %s", prefix, node_command_domain.RedactURLCredentialsInDetails(msg))
}
