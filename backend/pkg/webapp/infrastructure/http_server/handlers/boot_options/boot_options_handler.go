package boot_options

import (
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/labstack/echo/v4"
)

// BootOptionsHandler handles boot options related HTTP requests
type BootOptionsHandler struct {
	logger *slog.Logger
}

// NewBootOptionsHandler creates a new boot options handler
func NewBootOptionsHandler(logger *slog.Logger) *BootOptionsHandler {
	return &BootOptionsHandler{
		logger: logger.With("handler", "boot_options"),
	}
}

// RegisterRoutes registers boot options routes
func (h *BootOptionsHandler) RegisterRoutes(e *echo.Echo) {
	// Public endpoint - no auth required
	e.GET("/api/v1/boot-options", h.GetBootOptions)
}

// GetBootOptions returns all available boot options
// @Summary Get boot options
// @Description Returns all available boot options for Raspberry Pi
// @Tags boot-options
// @Accept json
// @Produce json
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Boot options list"
// @Router /api/v1/boot-options [get]
func (h *BootOptionsHandler) GetBootOptions(c echo.Context) error {
	options := nodeDomain.GetAllBootOptions()
	return results.SendSuccess(c, 200, map[string]interface{}{
		"options": options,
	}, "Boot options retrieved successfully")
}
