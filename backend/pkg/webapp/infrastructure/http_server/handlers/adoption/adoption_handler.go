// Package adoption serves the UNAUTHENTICATED one-time adoption-code exchange:
// the one-command install link carries an opaque code instead of the adoption
// blob; the device trades it here (over TLS) for the config. Single-use +
// short TTL + hashed at rest; every failure is the same 404-shaped error so
// the endpoint is not an oracle.
package adoption

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Handler exchanges one-time adoption codes for adoption configs.
type Handler struct {
	nodeService services.NodeService
	cfg         *config.Config
	logger      *slog.Logger
}

// NewHandler constructs the adoption exchange handler.
func NewHandler(nodeService services.NodeService, cfg *config.Config, logger *slog.Logger) *Handler {
	if nodeService == nil || cfg == nil {
		panic("adoption: nodeService and cfg are required")
	}
	return &Handler{nodeService: nodeService, cfg: cfg, logger: logger.With("handler", "adoption")}
}

// RegisterRoutes wires the public exchange route behind a per-IP rate limit
// (same envelope as /auth: sustained 1 req/2s, burst 10) — codes are 128-bit
// random so brute force is hopeless, the limiter just keeps the endpoint from
// being free load.
func (h *Handler) RegisterRoutes(e *echo.Echo) {
	limiter := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "rate limit exceeded", nil)
		},
	})
	e.POST("/api/v1/adopt/exchange", h.Exchange, limiter)
}

type exchangeRequest struct {
	Code string `json:"code"`
}

// Exchange consumes a one-time adoption code
// @Summary Exchange a one-time adoption code for the adoption config
// @Description Consumes a single-use, short-lived code minted by POST /nodes/{id}/adoption-code and returns the base64 adoption blob as text/plain (install.sh writes it verbatim to the adoption file). Unknown, expired and already-used codes fail identically.
// @Tags adoption
// @Accept json
// @Produce plain
// @Param request body exchangeRequest true "The one-time code"
// @Success 200 {string} string "base64 adoption blob"
// @Failure 404 {object} results.APIResponse "Code is invalid or expired"
// @Failure 429 {object} results.APIResponse "Rate limit exceeded"
// @Router /api/v1/adopt/exchange [post]
func (h *Handler) Exchange(c echo.Context) error {
	var req exchangeRequest
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		// Same shape as an invalid code — no request-format oracle either.
		return results.SendNotFoundError(c, app_errors.ErrAdoptionCodeInvalid.Error())
	}

	var platformURL string
	if h.cfg.Server.AdvertisementURL != "" {
		platformURL = h.cfg.Server.AdvertisementURL
	} else {
		platformURL = c.Scheme() + "://" + c.Request().Host
	}
	grpcEndpoint := h.cfg.GRPC.AdvertisementURL
	if grpcEndpoint == "" {
		grpcEndpoint = net.JoinHostPort(h.cfg.GRPC.Host, fmt.Sprintf("%d", h.cfg.GRPC.Port))
	}

	// Every failure (unknown/expired/used code, missing node, DB error) is a
	// uniform 404 so the unauthenticated endpoint is not an existence oracle;
	// the service logs any non-negative-path error itself.
	blob, err := h.nodeService.ExchangeAdoptionCode(c.Request().Context(), strings.TrimSpace(req.Code), platformURL, grpcEndpoint)
	if err != nil {
		return results.SendNotFoundError(c, app_errors.ErrAdoptionCodeInvalid.Error())
	}
	return c.String(http.StatusOK, blob)
}
