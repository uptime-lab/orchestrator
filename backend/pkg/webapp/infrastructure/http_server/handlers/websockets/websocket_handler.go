package websockets

import (
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"

	"github.com/labstack/echo/v4"
)

// WebSocketHandler handles WebSocket connections
type WebSocketHandler struct {
	wsManager *websockets.WebSocketManager
	logger    *slog.Logger
	cfg       *config.Config
}

// NewWebSocketHandler creates a new WebSocket handler
func NewWebSocketHandler(wsManager *websockets.WebSocketManager, logger *slog.Logger, cfg *config.Config) *WebSocketHandler {
	return &WebSocketHandler{
		wsManager: wsManager,
		logger:    logger.With("handler", "websocket"),
		cfg:       cfg,
	}
}

// Register adds WebSocket routes to the Echo instance
func (h *WebSocketHandler) Register(e *echo.Echo) {
	e.GET("/ws", h.HandleWebSocket)
	h.logger.Info("registered websocket routes")
}

// HandleWebSocket manages WebSocket connections
func (h *WebSocketHandler) HandleWebSocket(c echo.Context) error {
	h.logger.Debug("handling new websocket connection request", "client_ip", c.RealIP())
	return h.wsManager.HandleConnection(c)
}
