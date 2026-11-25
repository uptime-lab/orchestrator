package workflow_execution

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Why this test exists. Every http_request block built its own
// http.Transport for one request. The response came back on a keep-alive
// connection that the transport then parked in its idle pool — and nothing
// ever emptied that pool, because the transport itself was dropped right
// after. Neither the request context nor http.Client.Timeout retires an idle
// keep-alive, so each completed block left one descriptor and one goroutine
// behind until the peer closed the socket. Ten small requests opened ten
// connections and kept all ten.

// connCounter watches a test server's connections.
type connCounter struct {
	mu   sync.Mutex
	open int
	peak int
}

func (c *connCounter) state(_ net.Conn, st http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch st {
	case http.StateNew:
		c.open++
		if c.open > c.peak {
			c.peak = c.open
		}
	case http.StateClosed:
		c.open--
	}
}

func (c *connCounter) snapshot() (open, peak int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open, c.peak
}

func (c *connCounter) waitOpen(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if open, _ := c.snapshot(); open == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	open, _ := c.snapshot()
	t.Fatalf("server still holds %d connections, want %d", open, want)
}

func newCountingServer(t *testing.T) (*httptest.Server, *connCounter) {
	t.Helper()
	counter := &connCounter{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = counter.state
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, counter
}

// doAndDrain is what execHTTPRequest does with a response: read it and close
// the body, which is exactly the sequence that parks the connection.
func doAndDrain(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// plainDial is the unguarded dial the test transport needs: safeDial refuses
// loopback by design, which is the SSRF guard, not the thing under test here.
func plainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestSharedSafeTransportKeepsCompletedRequestsFromAccumulatingConnections(t *testing.T) {
	srv, counter := newCountingServer(t)
	const requests = 10

	// The leaky shape: a transport per request, keep-alives on,
	// nothing closing them. This is the control that proves the assertion
	// below can see the leak.
	leaky := make([]*http.Transport, 0, requests)
	for i := 0; i < requests; i++ {
		tr := &http.Transport{DialContext: plainDial}
		leaky = append(leaky, tr)
		doAndDrain(t, &http.Client{Timeout: 5 * time.Second, Transport: tr}, srv.URL)
	}
	if open, _ := counter.snapshot(); open != requests {
		t.Fatalf("control: expected the per-request transports to leave %d connections open, saw %d", requests, open)
	}
	for _, tr := range leaky {
		tr.CloseIdleConnections()
	}
	counter.waitOpen(t, 0)

	// The fix: one bounded transport shared by every block.
	shared := newSafeTransport(plainDial)
	t.Cleanup(shared.CloseIdleConnections)
	for i := 0; i < requests; i++ {
		client := &http.Client{Timeout: 5 * time.Second, Transport: shared,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		doAndDrain(t, client, srv.URL)
	}
	open, _ := counter.snapshot()
	if open > safeHTTPMaxIdleConnsPerHost {
		t.Fatalf("%d sequential requests through the shared transport left %d connections open, want at most %d",
			requests, open, safeHTTPMaxIdleConnsPerHost)
	}
	shared.CloseIdleConnections()
	counter.waitOpen(t, 0)
}

func TestSafeTransportIsSharedAndBounded(t *testing.T) {
	// Two clients built the production way must ride ONE transport, and that
	// transport must retire idle connections on its own — otherwise a block
	// that runs once an hour still holds its socket for the hour.
	a := newSafeHTTPClient(time.Second)
	b := newSafeHTTPClient(2 * time.Second)
	if a.Transport != b.Transport {
		t.Fatal("each client built its own transport; the idle pool is a sum again")
	}
	tr, ok := a.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", a.Transport)
	}
	if tr.IdleConnTimeout <= 0 {
		t.Fatal("no IdleConnTimeout: an idle keep-alive would be held until the peer closes it")
	}
	if tr.MaxIdleConns <= 0 || tr.MaxIdleConnsPerHost <= 0 {
		t.Fatalf("idle pool unbounded: MaxIdleConns=%d MaxIdleConnsPerHost=%d", tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
	}
	if a.Timeout != time.Second || b.Timeout != 2*time.Second {
		t.Fatal("the per-block timeout must stay on the client, not on the shared transport")
	}
}

func TestSharedSafeTransportStillRefusesBlockedAddresses(t *testing.T) {
	// Sharing the transport must not have moved the guard off the dial path.
	srv, _ := newCountingServer(t)
	client := newSafeHTTPClient(2 * time.Second)
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("a loopback server was reached through the production transport")
	}
}
