package grpc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

func hostnameServer() *Server {
	return &Server{
		config:      &config.Config{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		connections: map[string]*StreamConnection{},
	}
}

func setHostnameCommand(hostname string) *node_command.NodeCommand {
	return &node_command.NodeCommand{
		Slug:      node_command.SlugSetHostname,
		AccountID: uuid.New(),
		NodeID:    uuid.New(),
		Payload:   map[string]interface{}{"hostname": hostname},
	}
}

// A hostname the device will refuse must never be dispatched: the agent answers
// with the value quoted back inside its failure report, and that report is
// stored as plaintext node_commands.details every node.read caller can list.
// The value is routinely a resolved {{secrets.*}} — that is exactly the shape
// that fails an RFC 1123 label check.
func TestAnUnacceptableHostnameIsNotDispatchedToTheDevice(t *testing.T) {
	srv := hostnameServer()
	const secret = "S3cret-Token-Value"

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), setHostnameCommand(secret))
	if err == nil {
		t.Fatalf("an invalid hostname was built into a device command: %#v", cmd.GetSetHostname())
	}
	// The build error is written verbatim into the command's details by the
	// dispatch loop, so it must not name the value either.
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refused value was quoted into the dispatch error: %q", err)
	}
}

func TestAValidHostnameIsStillDispatchedTrimmed(t *testing.T) {
	srv := hostnameServer()

	cmd, err := srv.buildProtoCommandFromNodeCommand(context.Background(), setHostnameCommand("  node-01 "))
	if err != nil {
		t.Fatalf("a valid hostname was refused: %v", err)
	}
	if got := cmd.GetSetHostname().GetHostname(); got != "node-01" {
		t.Errorf("dispatched hostname = %q, want %q", got, "node-01")
	}
}
