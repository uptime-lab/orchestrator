package swagger

import (
	"log/slog"

	_ "github.com/uptime-lab/orchestrator/backend/docs" // This imports the generated docs
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/labstack/echo/v4"
	echoSwagger "github.com/swaggo/echo-swagger"
)

// SwaggerHandler handles Swagger documentation routes
type SwaggerHandler struct {
	logger *slog.Logger
	cfg    *config.Config
}

// NewSwaggerHandler creates a new Swagger documentation handler
func NewSwaggerHandler(logger *slog.Logger, cfg *config.Config) *SwaggerHandler {
	return &SwaggerHandler{
		logger: logger,
		cfg:    cfg,
	}
}

// Register adds Swagger documentation routes to the Echo instance
// @title Orchestrator Platform API
// @version 1.0
// @description API server for Orchestrator platform
// @contact.name API Support
// @contact.email support@orchestrator.sh
// @host localhost:8080
// @BasePath /
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and the JWT token.
func (h *SwaggerHandler) Register(e *echo.Echo) {
	// The complete route map is a gift to an attacker and of no use to anyone
	// on a production instance — the API is documented for developers, who run
	// with an explicit development environment. The handler already held the
	// config; it just never consulted it.
	if h.cfg != nil && h.cfg.IsProduction() {
		h.logger.Info("swagger UI disabled in production")
		return
	}
	h.logger.Info("registering swagger routes")
	e.GET("/swagger/*", echoSwagger.WrapHandler)
}
