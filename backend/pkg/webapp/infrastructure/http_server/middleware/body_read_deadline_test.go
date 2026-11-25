package middleware

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

const testIdle = 200 * time.Millisecond

// bodyReadServer starts a real HTTP server (a recorder cannot carry a read
// deadline) whose single route reads its whole body and reports how that went.
// A request to /released/... releases the deadline before reading, the way an
// authenticated streaming handler does.
func bodyReadServer(t *testing.T) *httptest.Server {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.Use(BodyReadIdleTimeout(testIdle, slog.New(slog.NewTextHandler(io.Discard, nil))))
	e.POST("/*", func(c echo.Context) error {
		if strings.HasPrefix(c.Request().URL.Path, "/released/") {
			ReleaseBodyReadDeadline(c)
		}
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("read %d", len(body)))
	})

	server := httptest.NewUnstartedServer(e)
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// writeHeaders announces a body of contentLength bytes and sends none of it.
func writeHeaders(t *testing.T, conn net.Conn, path string, contentLength int) {
	t.Helper()
	request := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: test\r\nContent-Type: application/octet-stream\r\n"+
		"Content-Length: %d\r\n\r\n", path, contentLength)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("failed to write request headers: %v", err)
	}
}

// A client that sends valid headers and then stops mid-body must not be able to
// hold the connection open. ReadHeaderTimeout ends at the headers and BodyLimit
// bounds only SIZE, so before this middleware the handler blocked forever.
func TestStalledBodyIsAbandonedRatherThanHeldForever(t *testing.T) {
	server := bodyReadServer(t)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	writeHeaders(t, conn, "/stalled", 64)
	if _, err := conn.Write([]byte("only-a-few-bytes")); err != nil {
		t.Fatalf("failed to write the partial body: %v", err)
	}

	// ...and then say nothing at all. The server must give up on its own, and
	// the response must actually reach the wire — net/http drains an unread body
	// before flushing the response, so a deadline that does not cover that drain
	// only moves the stall out of the handler.
	type result struct {
		body string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(20 * testIdle))
		response, err := io.ReadAll(conn)
		done <- result{string(response), err}
	}()

	select {
	case got := <-done:
		var netErr net.Error
		if errors.As(got.err, &netErr) && netErr.Timeout() {
			t.Fatal("the server never answered a stalled request body: the read deadline is not being applied")
		}
		if !strings.Contains(got.body, "408 Request Timeout") {
			t.Fatalf("a stalled request body did not get a 408: %q (err %v)", got.body, got.err)
		}
	case <-time.After(30 * testIdle):
		t.Fatal("the server never gave up on a stalled request body")
	}
}

// The other half, and the one that must not regress: a body that keeps arriving
// slowly is legitimate and must complete however long it takes in total. This is
// why the deadline is refreshed per read rather than set once for the request.
func TestSlowButMovingBodyIsNotKilled(t *testing.T) {
	server := bodyReadServer(t)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	const chunks = 8
	writeHeaders(t, conn, "/slow", chunks)

	// Total transfer time is several times the idle bound, but no single gap
	// reaches it.
	for i := 0; i < chunks; i++ {
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatalf("failed to write body byte %d: %v", i, err)
		}
		time.Sleep(testIdle / 2)
	}

	if err := conn.SetReadDeadline(time.Now().Add(20 * testIdle)); err != nil {
		t.Fatalf("failed to set the client read deadline: %v", err)
	}
	response := make([]byte, 256)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatalf("a slow but progressing upload was killed: %v", err)
	}
	if got := string(response[:n]); !strings.Contains(got, "200 OK") || !strings.Contains(got, fmt.Sprintf("read %d", chunks)) {
		t.Fatalf("a slow but progressing upload did not complete cleanly: %q", got)
	}
}

// The streaming uploads are not exempt by ROUTE any more; a
// handler releases the deadline once its caller is authenticated, and from
// then on the body may pace itself by a device reading a disk. The release
// has to survive the next read — every read re-arms the deadline, so a
// release that only cleared the connection would last until the first block.
func TestReleasedBodyMayStallPastTheIdleBound(t *testing.T) {
	server := bodyReadServer(t)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	writeHeaders(t, conn, "/released/capture", 2)
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("failed to write the first body byte: %v", err)
	}

	// Stall well past the idle bound, then finish. A released body must still
	// be waiting.
	time.Sleep(4 * testIdle)
	if _, err := conn.Write([]byte("y")); err != nil {
		t.Fatalf("failed to write the second body byte: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(20 * testIdle)); err != nil {
		t.Fatalf("failed to set the client read deadline: %v", err)
	}
	response := make([]byte, 256)
	n, err := conn.Read(response)
	if err != nil {
		t.Fatalf("a released streaming upload was killed: %v", err)
	}
	if got := string(response[:n]); !strings.Contains(got, "200 OK") || !strings.Contains(got, "read 2") {
		t.Fatalf("a released streaming upload did not complete cleanly: %q", got)
	}
}

// ...and the same stall on a route that did NOT release is still caught. Without
// this, ReleaseBodyReadDeadline could be implemented as "release everything" and
// the test above would still pass.
func TestUnreleasedBodyStallingPastTheIdleBoundIsCaught(t *testing.T) {
	server := bodyReadServer(t)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	writeHeaders(t, conn, "/capture", 2)
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("failed to write the first body byte: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(20 * testIdle))
	response, readErr := io.ReadAll(conn)
	var netErr net.Error
	if errors.As(readErr, &netErr) && netErr.Timeout() {
		t.Fatal("an unreleased stalled body was not caught")
	}
	if !strings.Contains(string(response), "408 Request Timeout") {
		t.Fatalf("an unreleased stalled body did not get a 408: %q", response)
	}
}

// Releasing where nothing was armed is a no-op, not a panic: a bodyless
// request, a disabled deadline and HTTP/2 all reach the handler without a
// wrapper on the context.
func TestReleaseWithoutAnArmedDeadlineIsANoOp(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/anything", http.NoBody)
	c := e.NewContext(req, httptest.NewRecorder())
	ReleaseBodyReadDeadline(c)
}

// productionChain mirrors the real middleware order in NewServer: Echo's
// BodyLimit is registered BEFORE this deadline. That ordering is the whole point
// of these two tests — BodyLimit replaces req.Body with its own *limitedReader on
// every request, so any check against http.NoBody stops matching, and a bodyless
// GET is no longer recognised as bodyless.
func productionChain(t *testing.T, handler echo.HandlerFunc) *httptest.Server {
	t.Helper()

	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{Limit: "32M"}))
	e.Use(BodyReadIdleTimeout(testIdle, slog.New(slog.NewTextHandler(io.Discard, nil))))
	e.GET("/stream", handler)
	e.POST("/stream", handler)

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return server
}

// slowWork mirrors the CDN handler: it takes longer than the idle bound and,
// crucially, it reads its SOURCE through the request context. Whether the request
// context survives is the whole property — a handler that ignores the context
// finishes fine even with the deadline armed, which is exactly why an earlier
// version of these tests passed while production was broken.
//
// The mechanism, from net/http/server.go: for a request with no body the server
// calls connReader.startBackgroundRead() BEFORE ServeHTTP, which clears the read
// deadline and leaves a goroutine blocked on a conn read. A deadline installed
// afterwards lands on THAT read; when it fires, handleReadErrorLocked calls
// conn.cancelCtx(), so the request context dies while the handler is mid-stream.
func slowWork(c echo.Context) error {
	select {
	case <-time.After(4 * testIdle):
		return c.String(http.StatusOK, "completed")
	case <-c.Request().Context().Done():
		return c.String(http.StatusOK, "context canceled: "+c.Request().Context().Err().Error())
	}
}

// A bodyless GET whose response outlives the idle bound must complete.
//
// Getting this wrong makes an OS install fail with "xz: (stdin): Unexpected end
// of input", which reads as a corrupt image; the cause would be every /cdn
// download cut at the idle bound. A deadline installed on a request with no
// body is never refreshed, because only a body read refreshes it.
//
// The earlier tests in this file all POST a body AND run without BodyLimit in
// front, which is exactly why none of them caught it.
func TestABodylessRequestKeepsItsContextForALongResponse(t *testing.T) {
	server := productionChain(t, slowWork)

	response, err := http.Get(server.URL + "/stream")
	if err != nil {
		t.Fatalf("the request failed outright: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the response failed: %v", err)
	}
	if got := string(body); got != "completed" {
		t.Fatalf("the request context was killed mid-handler: %q — a bodyless request must not get a read deadline", got)
	}
}

// The same hazard one step further in: a request that DOES carry a body, reads it
// promptly, and then spends longer than the idle bound producing its response.
// The last refresh happened at the last body read, so the deadline fires during
// the response. Gating installation on "has a body" alone would leave this half
// broken.
func TestASlowResponseAfterTheBodyWasReadKeepsItsContext(t *testing.T) {
	server := productionChain(t, func(c echo.Context) error {
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			return err
		}
		return slowWork(c)
	})

	response, err := http.Post(server.URL+"/stream", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("the request failed outright: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the response failed: %v", err)
	}
	if got := string(body); got != "completed" {
		t.Fatalf("the request context was killed after the body was read: %q", got)
	}
}

// ...and the guard must still fire for the shape it exists for, with BodyLimit in
// front. Without this, "skip when there is no body" could be implemented as "skip
// always" and every test above would still pass.
func TestStalledBodyIsStillCaughtBehindBodyLimit(t *testing.T) {
	server := productionChain(t, func(c echo.Context) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("read %d", len(body)))
	})

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial the test server: %v", err)
	}
	defer conn.Close()

	writeHeaders(t, conn, "/stream", 64)
	if _, err := conn.Write([]byte("only-a-few-bytes")); err != nil {
		t.Fatalf("failed to write the partial body: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(20 * testIdle))
	response, readErr := io.ReadAll(conn)
	var netErr net.Error
	if errors.As(readErr, &netErr) && netErr.Timeout() {
		t.Fatal("a stalled body was not caught behind BodyLimit: the deadline is not being installed")
	}
	if !strings.Contains(string(response), "408 Request Timeout") {
		t.Fatalf("a stalled body did not get a 408: %q", response)
	}
}

// 0 is the repo's "unlimited" convention, and an operator must be able to reach
// it without a rebuild breaking anything.
func TestZeroIdleDisablesTheDeadline(t *testing.T) {
	called := false
	handler := BodyReadIdleTimeout(0, slog.New(slog.NewTextHandler(io.Discard, nil)))(func(c echo.Context) error {
		called = true
		return nil
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/anything", http.NoBody)
	if err := handler(e.NewContext(req, httptest.NewRecorder())); err != nil {
		t.Fatalf("the pass-through returned an error: %v", err)
	}
	if !called {
		t.Fatal("a zero idle must be a pass-through")
	}
	if _, wrapped := req.Body.(*idleDeadlineBody); wrapped {
		t.Fatal("a zero idle wrapped the body anyway")
	}
}
