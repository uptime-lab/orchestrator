package image_create

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// ImageCreateCommand represents a command to create a new image
type ImageCreateCommand struct {
	ID          uuid.UUID            `json:"-"` // Set internally, not from request
	Title       string               `json:"title"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	ImageURL    string               `json:"image_url"`
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}
