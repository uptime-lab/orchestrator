package http_server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	appmiddleware "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/wsorigin"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Server represents the HTTP server
type Server struct {
	echo   *echo.Echo
	logger *slog.Logger
	cfg    *config.Config
	ws     *websockets.WebSocketManager
}

// corsWithLogging is a custom middleware that adds logging for CORS rejections
func corsWithLogging(corsConfig middleware.CORSConfig, logger *slog.Logger) echo.MiddlewareFunc {
	corsMiddleware := middleware.CORSWithConfig(corsConfig)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			// Check if it's a CORS request
			origin := req.Header.Get("Origin")
			if origin != "" {
				// Check if the origin is allowed
				allowed := false

				// If AllowOrigins contains "*", then any origin is allowed
				if len(corsConfig.AllowOrigins) > 0 && corsConfig.AllowOrigins[0] == "*" {
					allowed = true
				} else {
					// Check if the origin matches any of the allowed origins
					for _, allowedOrigin := range corsConfig.AllowOrigins {
						// Handle exact match
						if allowedOrigin == origin {
							allowed = true
							break
						}

						// Handle wildcard patterns like "chrome-extension://*"
						if strings.HasSuffix(allowedOrigin, "*") {
							prefix := strings.TrimSuffix(allowedOrigin, "*")
							if strings.HasPrefix(origin, prefix) {
								allowed = true
								break
							}
						}
					}
				}

				// Log rejected CORS requests. WHETHER a credential rode along
				// is what makes a rejected origin worth looking at; the value
				// never is. This line used to carry the whole header map —
				// the bearer, the OIDC cookies, the step-up ticket — into
				// stdout and every log backup.
				if !allowed {
					logger.Warn("CORS request rejected",
						"origin", origin,
						"allowed_origins", strings.Join(corsConfig.AllowOrigins, ", "),
						"method", req.Method,
						"path", req.URL.Path,
						"remote_addr", req.RemoteAddr,
						"has_authorization", req.Header.Get("Authorization") != "",
						"has_cookie", req.Header.Get("Cookie") != "",
						"has_reauth_ticket", req.Header.Get(appmiddleware.ReauthTicketHeader) != "")
				}
			}

			// Continue with the standard CORS middleware
			return corsMiddleware(next)(c)
		}
	}
}

// skipStreamingUpload matches the two routes whose request body is a stream of
// unbounded length and unpredictable pace: the image upload and the device
// capture upload. Both carry their own byte caps (http.MaxBytesReader on the
// upload, a streaming cap on the capture).
//
// It exempts the SIZE cap only. It must not be handed to BodyReadIdleTimeout
// again: it is evaluated on the raw path before routing and
// authentication, so it also skipped the deadline for an anonymous PUT that
// declared a body and sent nothing, for a wrong method and for a lookalike
// path under these prefixes — each held in net/http's post-handler drain with
// no bound. The streaming handlers release the deadline themselves, after
// their caller is authenticated (middleware.ReleaseBodyReadDeadline).
func skipStreamingUpload(c echo.Context) bool {
	p := c.Request().URL.Path
	return strings.HasPrefix(p, "/capture/") ||
		(strings.HasPrefix(p, "/api/v1/images/") && strings.HasSuffix(p, "/upload"))
}

// NewServer creates a new Echo server
func NewServer(authService services.AuthService, toClientsQueue chan websockets3.Message, logger *slog.Logger, cfg *config.Config) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// One error shape for the whole API: an error a handler or middleware
	// RETURNS is rendered in the same envelope the results.Send* helpers write
	// (see error_handler.go for why those sites return rather than write).
	e.HTTPErrorHandler = envelopeErrorHandler(logger)

	// Derive the client IP from X-Forwarded-For, trusting ONLY loopback and private
	// ranges (the Traefik proxy sits on the private docker network). Without this,
	// Echo's legacy RealIP trusts a client-supplied X-Forwarded-For, so an attacker
	// could rotate the header to defeat every per-IP rate limiter (auth brute-force,
	// netboot). With it, the real edge IP is the rightmost untrusted hop.
	e.IPExtractor = echo.ExtractIPFromXFFHeader(
		echo.TrustLoopback(true),
		echo.TrustLinkLocal(true),
		echo.TrustPrivateNet(true),
	)

	// Configure middleware.
	//
	// RequestID first, so the access log's "id" field is populated rather than
	// empty on every line — it is what ties a Traefik entry, this line, the
	// service-level slog lines and a node_commands row together when a user
	// reports something that happened yesterday.
	e.Use(middleware.RequestID())

	// ...and immediately carry that id in the request CONTEXT, so it reaches the
	// service layer. RequestID alone sets a header and an Echo-context value,
	// which only the access log can read — a service is handed a
	// context.Context, not an echo.Context, so the id appeared on exactly one
	// line per request while the lines explaining what happened had none.
	e.Use(appmiddleware.RequestScope())

	// The access log deliberately records ${path}, NOT ${uri}. Echo's default
	// format uses ${uri}, which is path PLUS query string — and live bearer
	// credentials travel as query params here: the invite token (7-day TTL, and
	// "the token IS the credential" per its own doc comment), the reauth and
	// passkey tickets, the OIDC code and state, and the ?exp=&sig= capability
	// HMACs on /cdn/* and PUT /capture/:id. Anyone who could read this log could
	// accept an invitation as someone else. Every other field is Echo's default
	// verbatim; the key is "path" rather than "uri" so the name matches what it
	// holds.
	e.Use(middleware.LoggerWithConfig(accessLogConfig()))
	e.Use(middleware.Recover())

	// Global request-body cap: every JSON/API route gets a hard body
	// limit enforced BEFORE the body is consumed, so a chunked request (no
	// Content-Length) cannot feed unbounded data into a handler. The two
	// streaming endpoints are exempt and carry their own byte caps: the image
	// upload wraps its body in http.MaxBytesReader against the configured image
	// limit, and the capture upload enforces its own streaming cap.
	e.Use(middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{
		Skipper: skipStreamingUpload,
		Limit:   "32M",
	}))

	// ...and a bound on TIME to go with that bound on SIZE.
	// BodyLimit lets a client take forever over its 32 MB, and
	// ReadHeaderTimeout stops at the end of the headers, so a dribbled body
	// pinned a goroutine and a connection indefinitely from any route that
	// takes one. This deadline is refreshed on every read that makes progress,
	// so it kills a stall rather than a slow transfer. It takes NO skipper, and
	// deliberately not the one above: that predicate runs on
	// the raw path before routing and authentication, so an anonymous client
	// could park a connection by declaring a body under /capture/ and sending
	// nothing. A capture legitimately goes quiet between blocks, so the two
	// streaming handlers RELEASE the deadline once their caller is
	// authenticated (middleware.ReleaseBodyReadDeadline) — an exemption that
	// exists only for a verified credential, never for a path.
	e.Use(appmiddleware.BodyReadIdleTimeout(cfg.Server.ReadTimeout,
		logger.With("server", "main")))

	// Add CORS middleware with configuration
	if cfg.Server.CORS.Enabled {
		corsConfig := middleware.CORSConfig{
			AllowOrigins:     cfg.Server.CORS.AllowedOrigins,
			AllowMethods:     cfg.Server.CORS.AllowedMethods,
			AllowHeaders:     cfg.Server.CORS.AllowedHeaders,
			AllowCredentials: cfg.Server.CORS.AllowCredentials,
			MaxAge:           cfg.Server.CORS.MaxAge,
		}
		e.Use(corsWithLogging(corsConfig, logger.With("component", "cors")))
	} else {
		// Default CORS if not specifically configured
		defaultCorsConfig := middleware.CORSConfig{
			AllowOrigins: []string{"*"},
		}
		e.Use(corsWithLogging(defaultCorsConfig, logger.With("component", "cors")))
	}

	// Create WebSocket manager. The socket origin policy is deliberately NOT the
	// CORS switch: a WS handshake is exempt from the same-origin policy, so
	// CheckOrigin is the only guard, and deriving it from CORS made it allow-all
	// on every install.
	wsManager := websockets.NewWebSocketManager(authService, toClientsQueue,
		wsorigin.New(cfg, logger.With("component", "wsorigin")), logger.With("server", "main"))
	wsManager.Run()

	// Liveness. Deliberately static and deliberately NOT a dependency check:
	// this is what a container healthcheck watches, and restarting the backend
	// repairs neither a dead database nor a full disk — it only turns an
	// outage into a restart loop, which is harder to diagnose and takes the
	// logs with it. The question "can this instance actually serve" is
	// `/ready`, registered by RegisterReadiness below.
	e.GET("/health", healthHandler)

	return &Server{
		echo:   e,
		logger: logger.With("server", "main"),
		cfg:    cfg,
		ws:     wsManager,
	}
}

// healthHandler is a named function purely so it can carry the annotation
// below: an inline closure has nowhere for `swag` to read one from, which is
// how the endpoint every container healthcheck polls came to be undocumented.
// @Summary Liveness
// @Description Static liveness answer. It deliberately checks NOTHING — this is what a container healthcheck polls, and restarting the backend repairs neither a dead database nor a full disk, so a dependency check here would turn an outage into a restart loop. Ask /ready whether the instance can actually serve.
// @Tags health
// @Produce json
// @Success 200 {object} map[string]string "Always, while the process is running"
// @Router /health [get]
func healthHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// ReadinessProbe reports whether this instance can serve requests that touch
// its dependencies. Named interface in this package rather than an import of
// the health package, so the HTTP layer does not depend on the checker.
type ReadinessProbe interface {
	Probe(ctx context.Context) (ready bool, body any)
}

// RegisterReadiness publishes `/ready`.
//
// Public and unauthenticated on purpose: its whole point is to be reachable by
// an external uptime probe, which holds no token — and the body says only
// which dependency is unhappy, never why. 503 rather than 200-with-a-status
// field, because every monitor and every proxy already understands the status
// code and none of them parse the body.
func (s *Server) RegisterReadiness(probe ReadinessProbe) {
	if probe == nil {
		panic("RegisterReadiness: probe is required")
	}
	s.echo.GET("/ready", readinessHandler(probe))
}

// readinessHandler carries the annotation for `/ready` for the same reason
// healthHandler does — the closure it returns has nowhere to put one.
// @Summary Readiness
// @Description Whether this instance can serve requests that touch its dependencies: a database ping plus free bytes AND free inodes on the storage path (replaced by a bucket reachability check when images live in object storage, because a statfs of a disk nothing writes to passes while every upload fails). Public and unauthenticated on purpose — an external uptime probe holds no token — and the body names which dependency is unhappy, never why. Cached for a few seconds.
// @Tags health
// @Produce json
// @Success 200 {object} map[string]interface{} "Ready"
// @Failure 503 {object} map[string]interface{} "A dependency is unhappy"
// @Router /ready [get]
func readinessHandler(probe ReadinessProbe) echo.HandlerFunc {
	return func(c echo.Context) error {
		ready, body := probe.Probe(c.Request().Context())
		if !ready {
			return c.JSON(http.StatusServiceUnavailable, body)
		}
		return c.JSON(http.StatusOK, body)
	}
}

// Start starts the HTTP server
func (s *Server) Start(addr string) error {
	// Real connection timeouts. ReadHeaderTimeout closes slowloris
	// header dribbles; IdleTimeout reaps idle keep-alive connections. Blanket
	// Read/Write timeouts are deliberately NOT set: a multi-gigabyte image
	// upload or a long-lived WebSocket/terminal stream legitimately outlives
	// any fixed deadline. The body half of the same problem is bounded by
	// BodyReadIdleTimeout in NewServer, which bounds a STALL rather than a
	// transfer and is therefore safe for those routes; a reverse proxy in
	// front must not bound them either (Traefik's respondingTimeouts.readTimeout
	// stays 0 for the same upload reason).
	s.echo.Server.ReadHeaderTimeout = s.cfg.Server.ReadTimeout
	s.echo.Server.IdleTimeout = 120 * time.Second
	s.logger.Info("starting Echo server", "address", addr)
	return s.echo.Start(addr)
}

// Stop stops the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("shutting down Echo server")
	return s.echo.Shutdown(ctx)
}

// Echo returns the echo instance
func (s *Server) Echo() *echo.Echo {
	return s.echo
}

// Logger returns the server's logger
func (s *Server) Logger() *slog.Logger {
	return s.logger
}

// WebSocketManager returns the WebSocket manager
func (s *Server) WebSocketManager() *websockets.WebSocketManager {
	return s.ws
}
