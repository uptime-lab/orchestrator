package node_file_update

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

func TestOversizedContentIsRefused(t *testing.T) {
	cmd := NodeFileUpdateCommand{
		NodeID:  uuid.New(),
		Path:    "/etc/hosts",
		Content: strings.Repeat("x", node_file.MaxContentBytes+1),
	}
	// The failure this prevents is not a big row: an oversized command frame
	// drops the node's gRPC stream, so the node goes offline rather than the
	// command failing.
	if err := cmd.Validate(); !errors.Is(err, node_file.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestContentAtTheLimitIsAccepted(t *testing.T) {
	cmd := NodeFileUpdateCommand{
		NodeID:  uuid.New(),
		Path:    "/etc/hosts",
		Content: strings.Repeat("x", node_file.MaxContentBytes),
	}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("expected exactly the limit to be accepted, got %v", err)
	}
}
