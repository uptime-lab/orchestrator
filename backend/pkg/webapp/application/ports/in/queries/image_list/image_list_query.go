package image_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// ImageListQuery represents a query to list all images for an account
type ImageListQuery struct {
	AuthContext commands.AuthContext
	// Optional pagination parameters can be added here in the future
	// Offset int
	// Limit  int
}
