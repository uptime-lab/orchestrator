package image_get

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// ImageGetQuery represents a query to get an image by ID
type ImageGetQuery struct {
	ID          uuid.UUID
	AuthContext commands.AuthContext
}
