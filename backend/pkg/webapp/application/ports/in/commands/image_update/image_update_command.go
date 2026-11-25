package image_update

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// ImageUpdateCommand represents a command to update an existing image
type ImageUpdateCommand struct {
	ID          uuid.UUID            `json:"-"` // Set internally, not from request
	Title       string               `json:"title"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	ImageURL    string               `json:"image_url"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}
