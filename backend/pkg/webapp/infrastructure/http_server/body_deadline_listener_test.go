package http_server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	ws_ports "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	domain_image "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	image_handler "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/image"
	appmiddleware "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// These tests exercise the REAL middleware chain (NewServer) and the REAL image
// handler routes over a real TCP listener, because the property under test
// lives below the handler: net/http drains an unread request body before the
// response reaches the wire, and that drain reads the raw connection. A
// recorder-based test cannot see any of it, and a test that wires its own
// chain would only prove something about the chain it wired.
//
// The hazard: both streaming routes used to be excluded from the
// body-read idle deadline by a raw-path predicate evaluated BEFORE routing and
// authentication. An unauthenticated client could declare a body, send
// nothing, and hold the connection for as long as it liked — the refusal was
// written into the buffer and then the post-handler drain blocked on the
// stalled client with no deadline. Wrong methods and lookalike paths under the
// same prefixes drained the same way.

// deadlineTestIdle is the idle bound the server under test is built with. Short,
// so a test that waits "well past" it still finishes in well under a second.
const deadlineTestIdle = 200 * time.Millisecond

// deadlineTestImageService stands in for the image service on the two routes
// under test. The capability check is the real HMAC (cdnsign) over the real
// name the service signs; FinalizeCapture consumes the stream and reports how
// much it read, which is what the positive control asserts on.
type deadlineTestImageService struct {
	services.ImageService
	key []byte
}

func (s deadlineTestImageService) VerifyCaptureUploadCapability(imageID uuid.UUID, expStr, sig string) error {
	return cdnsign.Verify("capture/"+imageID.String(), expStr, sig, s.key)
}

func (s deadlineTestImageService) FinalizeCapture(_ context.Context, imageID uuid.UUID, expStr, sig string, body io.Reader) (*domain_image.Image, error) {
	if err := cdnsign.Verify("capture/"+imageID.String(), expStr, sig, s.key); err != nil {
		return nil, err
	}
	n, err := io.Copy(io.Discard, body)
	if err != nil {
		return nil, err
	}
	return &domain_image.Image{ID: imageID, Title: fmt.Sprintf("read %d", n)}, nil
}

// connCounter counts live server-side connections through http.Server's
// ConnState hook, so a test can assert the server let go of the connection
// rather than only that the client stopped hearing from it.
type connCounter struct {
	mu   sync.Mutex
	live int
}

func (c *connCounter) hook(_ net.Conn, state http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch state {
	case http.StateNew:
		c.live++
	case http.StateClosed, http.StateHijacked:
		c.live--
	}
}

func (c *connCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

// deadlineTestServer builds the production chain and registers the two
// streaming routes on it, exactly as SetupRoutes does, then serves it on a
// real listener.
func deadlineTestServer(t *testing.T) (*httptest.Server, *connCounter, []byte) {
	t.Helper()

	cfg := routesTestConfig()
	cfg.Server.ReadTimeout = deadlineTestIdle
	key := cdnsign.DeriveKey(cfg.CDNSigningSecret())

	srv := NewServer(stubAuthService{}, make(chan ws_ports.Message, 1),
		slog.New(slog.DiscardHandler), cfg)
	e := srv.Echo()

	handler := image_handler.NewImageHandler(deadlineTestImageService{key: key},
		&audit_service.Recorder{}, cfg, slog.New(slog.DiscardHandler))
	handler.RegisterRoutes(e, appmiddleware.JWTAuth(cfg.JWT), &authz_service.Enforcer{})
	handler.RegisterCaptureRoutes(e)

	counter := &connCounter{}
	server := httptest.NewUnstartedServer(e)
	server.Config.ConnState = counter.hook
	server.Start()
	t.Cleanup(server.Close)
	return server, counter, key
}

// sendAndWaitForClose writes a raw request and then reads until the SERVER
// closes the connection. It returns what arrived and whether the close came
// within the wait; a client that stalls is exactly what the attack is, so the
// client here never sends another byte after the request.
func sendAndWaitForClose(t *testing.T, addr, raw string, wait time.Duration) (response string, closed bool) {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatalf("failed to write the request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	got, readErr := io.ReadAll(conn)
	var netErr net.Error
	if errors.As(readErr, &netErr) && netErr.Timeout() {
		return string(got), false
	}
	return string(got), true
}

// waitForNoLiveConnections gives the server up to a second past the client-side
// observation: net/http's closeWriteAndWait sends the FIN and then sleeps its
// rstAvoidanceDelay (500 ms) before the full close that ConnState reports, so
// "the client saw EOF" precedes "the server let go" by that much.
func waitForNoLiveConnections(t *testing.T, counter *connCounter) {
	t.Helper()
	const wait = time.Second
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if counter.count() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server still holds %d connection(s) after %s", counter.count(), wait)
}

// assertRefusedAndReleased is the shape every negative case shares: the
// refusal must reach the wire (it used to sit in the buffer behind the drain),
// and the connection must be gone within a small multiple of the idle bound.
func assertRefusedAndReleased(t *testing.T, server *httptest.Server, counter *connCounter, raw, wantStatus string) {
	t.Helper()
	response, closed := sendAndWaitForClose(t, server.Listener.Addr().String(), raw, 4*deadlineTestIdle)
	if !strings.Contains(response, wantStatus) {
		t.Fatalf("expected %q to reach the wire, got %q", wantStatus, response)
	}
	if !closed {
		t.Fatalf("the server kept the connection open past 4x the idle bound after answering %q", wantStatus)
	}
	waitForNoLiveConnections(t, counter)
}

// An unauthenticated PUT to the capture route with a declared body it never
// sends. The signature is garbage, so the handler refuses without reading —
// and before the fix that refusal never left the server: with the deadline
// skipped by the path predicate, net/http's drain of the unread 200 000 bytes
// (under its 256 KiB early-close threshold) blocked on the silent client.
func TestUnauthenticatedCaptureStallIsBoundedByTheIdleDeadline(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := fmt.Sprintf("PUT /capture/%s?exp=1&sig=x HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: 200000\r\n\r\n", uuid.New())
	assertRefusedAndReleased(t, server, counter, raw, "403 Forbidden")
}

// The same with a chunked body and no chunks: no declared length, so the
// early-close threshold cannot save it either — the drain waits for a chunk
// that never comes.
func TestUnauthenticatedChunkedCaptureStallIsBounded(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := fmt.Sprintf("PUT /capture/%s?exp=1&sig=x HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n", uuid.New())
	assertRefusedAndReleased(t, server, counter, raw, "403 Forbidden")
}

// The JWT-guarded image upload: no token, so the route middleware answers 401
// before the handler runs. The refusal must not be held hostage by the body
// the client declared and withheld.
func TestUnauthenticatedImageUploadStallIsBounded(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := fmt.Sprintf("POST /api/v1/images/%s/upload HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: 200000\r\n\r\n", uuid.New())
	assertRefusedAndReleased(t, server, counter, raw, "401 Unauthorized")
}

// Echo routes BEFORE the e.Use chain, so a wrong method under the exempt
// prefix reaches the deadline middleware as Echo's 405 handler — with the path
// predicate it was skipped exactly like the real route, and drained exactly
// the same way.
func TestWrongMethodUnderCapturePrefixIsBounded(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := fmt.Sprintf("GET /capture/%s HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: 200000\r\n\r\n", uuid.New())
	assertRefusedAndReleased(t, server, counter, raw, "405 Method Not Allowed")
}

// A lookalike path under the prefix. Measured rather than assumed: Echo's
// router hands /capture/a/b to the :id route with id "a/b", so this is the
// handler's own 400 for an unparseable id — refused, like the bad signature,
// before a byte of body is read, and drained the same way afterwards. What
// matters is not the number but that it reaches the wire and the connection
// goes.
func TestLookalikePathUnderCapturePrefixIsBounded(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := "PUT /capture/a/b HTTP/1.1\r\nHost: test\r\n" +
		"Content-Type: application/octet-stream\r\nContent-Length: 200000\r\n\r\n"
	assertRefusedAndReleased(t, server, counter, raw, "400 Bad Request")
}

// ...and the same shape under the image-upload prefix, which the predicate
// matched by prefix AND suffix, so anything between them was exempt too. The
// images group carries the JWT middleware on its catch-all, hence 401.
func TestLookalikePathUnderImageUploadPrefixIsBounded(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := "POST /api/v1/images/a/b/upload HTTP/1.1\r\nHost: test\r\n" +
		"Content-Type: application/octet-stream\r\nContent-Length: 200000\r\n\r\n"
	assertRefusedAndReleased(t, server, counter, raw, "401 Unauthorized")
}

// A declared length ABOVE net/http's 256 KiB post-handler drain threshold
// closes at once, with or without the deadline: the handler closes the body,
// net/http sees more unread bytes than it is willing to drain, gives up on the
// connection and sends the refusal with the close. This test pins that the
// deadline work did not slow that path down — it passed before the fix and
// must keep passing, so nobody later "repairs" the instant close into a wait.
func TestOversizedDeclarationClosesImmediately(t *testing.T) {
	server, counter, _ := deadlineTestServer(t)
	raw := fmt.Sprintf("PUT /capture/%s?exp=1&sig=x HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", uuid.New(), 10<<20)

	started := time.Now()
	response, closed := sendAndWaitForClose(t, server.Listener.Addr().String(), raw, 4*deadlineTestIdle)
	if !strings.Contains(response, "403 Forbidden") || !closed {
		t.Fatalf("an oversized declaration was not refused and closed: %q (closed %v)", response, closed)
	}
	if elapsed := time.Since(started); elapsed >= deadlineTestIdle {
		t.Fatalf("an oversized declaration took %s to close; it must not wait for the idle deadline", elapsed)
	}
	waitForNoLiveConnections(t, counter)
}

// The positive control, and the flow the exemption existed to protect: a
// capture with a VALID capability may go quiet between blocks for longer than
// the idle bound — the device is reading a disk, not a browser — and must
// still complete. The deadline is released once the capability has been
// verified, and only then.
func TestAuthenticatedCaptureMayStallBetweenBlocks(t *testing.T) {
	server, _, key := deadlineTestServer(t)

	imageID := uuid.New()
	exp, sig := cdnsign.Sign("capture/"+imageID.String(), key, time.Minute)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	const first, second = "first-block", "second-block"
	raw := fmt.Sprintf("PUT /capture/%s?exp=%s&sig=%s HTTP/1.1\r\nHost: test\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n%s",
		imageID, exp, sig, len(first)+len(second), first)
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatalf("failed to write the request and first block: %v", err)
	}

	// Well past the idle bound, then the rest.
	time.Sleep(4 * deadlineTestIdle)
	if _, err := conn.Write([]byte(second)); err != nil {
		t.Fatalf("failed to write the second block: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(4 * deadlineTestIdle))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("an authenticated capture that paused between blocks was killed: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "200 OK") || !strings.Contains(got, fmt.Sprintf("read %d", len(first)+len(second))) {
		t.Fatalf("an authenticated capture did not complete cleanly: %q", got)
	}
}
