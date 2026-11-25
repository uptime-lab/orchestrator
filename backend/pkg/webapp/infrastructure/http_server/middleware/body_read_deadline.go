package middleware

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
)

// BodyReadIdleTimeout bounds how long the server waits for the NEXT byte of a
// request body — not how long the whole body may take.
//
// That distinction is the entire design. A blanket http.Server.ReadTimeout
// bounds the whole request, so it would kill a multi-gigabyte image upload or a
// whole-disk capture the moment it outlived a fixed deadline; the server sets
// none for exactly that reason (see Server.Start). But ReadHeaderTimeout stops
// at the end of the headers and Echo's BodyLimit bounds SIZE, so nothing bounded
// TIME once the body started: a client that sent valid headers and then dribbled
// its body held a goroutine, a connection and an upstream Traefik connection for
// as long as it liked, from any route that takes a body — including the
// unauthenticated ones. The deadline installed here is refreshed on every read,
// so a slow-but-moving body never trips it and a stalled one dies.
//
// idle <= 0 disables it, following the repo's 0 = unlimited convention.
//
// It has NO route skipper, and that absence is a rule. The two
// streaming uploads used to be exempted by a raw-path predicate, evaluated
// before routing and before authentication — so an unauthenticated PUT under
// /capture/ that declared a body and sent nothing was refused by the handler
// and then held the connection in net/http's post-handler drain, with no
// deadline, for as long as the client liked; a wrong method or a lookalike path
// under the same prefixes drained the same way. The deadline is therefore armed
// for EVERY request that carries a body, and a streaming handler RELEASES it
// (ReleaseBodyReadDeadline) once, and only once, its caller is authenticated:
// after that a device reading a disk may go quiet between blocks for as long as
// it needs. What is left uncovered is an authenticated stall, one goroutine per
// valid credential, which is the exposure the exemption was always meant to
// accept.
//
// The deadline lives on the CONNECTION and is deliberately NOT cleared when the
// handler returns, which is the non-obvious half. net/http clears it itself as
// soon as the body hits EOF (connReader.startBackgroundRead) and again on a
// hijack, so a handler doing long work after reading its body, and the
// WebSocket/terminal upgrades, are unaffected either way. What a clear here
// WOULD break is the stall itself: after the handler returns, chunkWriter
// .writeHeader drains up to 256 KiB of an unread body before the response
// reaches the wire, and that drain reads the ORIGINAL body, past this wrapper.
// With no deadline it blocks forever on the same client that stalled — the
// response never leaves the buffer and the goroutine is pinned exactly as
// before. With the deadline inherited it fails, net/http sets closeAfterReply,
// and the response is still written.
func BodyReadIdleTimeout(idle time.Duration, logger *slog.Logger) echo.MiddlewareFunc {
	scoped := logger.With("component", "body_read_deadline")

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if idle <= 0 {
			return next
		}
		return func(c echo.Context) error {
			req := c.Request()
			if !requestCarriesABody(req) {
				return next(c)
			}

			controller := http.NewResponseController(c.Response())
			if err := controller.SetReadDeadline(time.Now().Add(idle)); err != nil {
				// HTTP/2 and httptest's recorder do not support deadlines. This
				// is defence in depth; it must never be the thing that fails a
				// request.
				return next(c)
			}

			body := &idleDeadlineBody{
				inner:    req.Body,
				idle:     idle,
				deadline: controller.SetReadDeadline,
			}
			req.Body = body
			c.Set(bodyReadDeadlineContextKey, body)

			// Hand the original body back once the handler is done. The
			// wrapper exists for the HANDLER's reads; what runs after it is
			// net/http's own post-handler drain, whose early-close logic is
			// written against its own body type (a handler that closed a
			// partial body, a declared length past the 256 KiB drain
			// threshold) and degrades to a plain drain when it meets a
			// wrapper it does not recognise. In the production chain
			// RequestScope has already copied the request, so this only
			// touches the copy; it is here so the property does not depend
			// on that ordering.
			defer func() { req.Body = body.inner }()

			err := next(c)
			if !body.expired.Load() {
				return err
			}

			scoped.WarnContext(req.Context(), "request body stalled; read deadline expired",
				"path", c.Path(), "method", req.Method, "idle", idle.String(),
				"bytes_read", body.read.Load(), "remote_ip", c.RealIP())

			// A handler that swallowed the read error and answered anyway has
			// already committed its response; turning that into a 408 now would
			// only produce a "response already committed" log line.
			if c.Response().Committed {
				return err
			}
			return echo.NewHTTPError(http.StatusRequestTimeout, "request body stalled")
		}
	}
}

// requestCarriesABody reports whether there is a body to bound at all.
//
// Installing the deadline on a request WITHOUT one is not merely useless, it is
// the bug this function exists to prevent. Two facts combine:
//
//  1. For a bodyless request, net/http calls connReader.startBackgroundRead()
//     BEFORE ServeHTTP (server.go, the requestBodyRemains branch). That clears
//     the read deadline and leaves a goroutine blocked reading the connection. A
//     deadline installed afterwards lands on THAT read, and when it expires
//     handleReadErrorLocked calls conn.cancelCtx() — so the REQUEST CONTEXT dies
//     mid-handler. Nothing refreshes it either, because the refresh lives in a
//     body read that never happens.
//  2. Every /cdn download reads its source through c.Request().Context(), so the
//     cancel truncates the response: a long image download is cut at the idle
//     bound, and the agent reports "xz: (stdin): Unexpected end of input", which
//     reads as a corrupt image rather than as a server timeout.
//
// The test that a request "has a body" must be ContentLength, NOT the identity of
// req.Body. Echo's BodyLimit is registered ahead of this middleware and replaces
// req.Body with its own *limitedReader on EVERY request, so `req.Body ==
// http.NoBody` — the obvious check — never matches in the real chain. It matched
// in a unit test that omitted BodyLimit, which is precisely how this shipped.
//
// ContentLength is untouched by any wrapper: 0 means no body, -1 means unknown
// (chunked), >0 means a declared body. Only the first is skipped.
func requestCarriesABody(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return false
	}
	return req.ContentLength != 0
}

// bodyReadDeadlineContextKey is where the middleware parks its wrapper so a
// handler can release it. A string key rather than a typed one because that
// is what every other middleware in this package uses (userID, accountID).
const bodyReadDeadlineContextKey = "bodyReadDeadline"

// ReleaseBodyReadDeadline lifts the idle deadline for the rest of this request.
//
// For the two streaming uploads only, and only AFTER the caller has been
// authenticated — the JWT and action middleware on the image upload, the HMAC
// capability on the capture. Before that point the deadline is what stops an
// anonymous client from parking a connection; after it the body's pace is set
// by a device reading a disk, which legitimately goes quiet between blocks for
// longer than any idle bound. Calling it from a handler that has not yet
// authenticated its caller reopens the window the deadline closes.
//
// A no-op when no deadline was installed (a bodyless request, idle disabled,
// HTTP/2). The release must land in the wrapper as well as on the connection:
// every read re-arms the deadline, so clearing it once through the controller
// alone would last exactly until the next read.
func ReleaseBodyReadDeadline(c echo.Context) {
	body, ok := c.Get(bodyReadDeadlineContextKey).(*idleDeadlineBody)
	if !ok || body == nil {
		return
	}
	body.release()
}

// idleDeadlineBody refreshes the connection's read deadline before every read,
// so the bound is on a stall rather than on the transfer.
type idleDeadlineBody struct {
	inner    io.ReadCloser
	idle     time.Duration
	deadline func(time.Time) error

	read     atomic.Int64
	expired  atomic.Bool
	released atomic.Bool
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	if b.released.Load() {
		return b.inner.Read(p)
	}
	if err := b.deadline(time.Now().Add(b.idle)); err != nil {
		return b.inner.Read(p)
	}
	n, err := b.inner.Read(p)
	b.read.Add(int64(n))
	if err != nil && isReadDeadlineError(err) {
		b.expired.Store(true)
	}
	return n, err
}

// release clears the connection deadline and stops Read from re-arming it. The
// flag is set FIRST, so a read racing the clear cannot re-install a deadline
// that then outlives the release.
func (b *idleDeadlineBody) release() {
	if b.released.Swap(true) {
		return
	}
	// Best effort, as the arming is: on a connection that cannot carry a
	// deadline nothing was armed in the first place.
	_ = b.deadline(time.Time{})
}

func (b *idleDeadlineBody) Close() error { return b.inner.Close() }

func isReadDeadlineError(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
