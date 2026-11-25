package node_file_update

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// NodeFileUpdateCommand represents a command to save a new version of a tracked
// file for a node. The path arrives in the request body — it used to be a
// wildcard route segment, which collided with the versions route.
type NodeFileUpdateCommand struct {
	NodeID      uuid.UUID            `json:"-"`
	Path        string               `json:"path"`
	Content     string               `json:"content"`
	AuthContext commands.AuthContext `json:"-"`
}

// Validate checks the command. The path goes through the same domain policy as
// tracking, so the editor cannot introduce a path the track endpoint refuses.
func (c *NodeFileUpdateCommand) Validate() error {
	if c.NodeID == uuid.Nil {
		return fmt.Errorf("node id is required")
	}
	path, err := node_file.ValidateTrackPath(c.Path)
	if err != nil {
		return err
	}
	c.Path = path
	// The read path has always been bounded; saving was not, so a caller could
	// store megabytes and only find out at Apply time — by which point the
	// oversized command frame has dropped the node's stream.
	if len(c.Content) > node_file.MaxContentBytes {
		return fmt.Errorf("%w: content is %d KB; the limit is %d KB",
			node_file.ErrFileTooLarge, len(c.Content)/1024, node_file.MaxContentBytes/1024)
	}
	return nil
}
