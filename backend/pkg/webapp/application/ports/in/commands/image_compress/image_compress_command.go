package image_compress

import (
	"fmt"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// ImageCompressCommand represents a command to compress an uploaded raw
// image file (.img/.iso) to .xz on the server.
type ImageCompressCommand struct {
	ID          uuid.UUID            `json:"-"` // Set from path parameter, not from request
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}

// Validate checks the command invariants.
func (c *ImageCompressCommand) Validate() error {
	if c.ID == uuid.Nil {
		return fmt.Errorf("image ID is required")
	}
	return nil
}
