package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// Backend half of the read_file bound: max_bytes is caller-supplied (generic
// command API, workflow blocks), and the agent reads up to that many bytes
// into memory before anything truncates — an unbounded value is an OOM in the
// RAM-rooted recovery initramfs or a stream-killing frame. The dispatch
// builder clamps it to node_file.MaxContentBytes; clamping at dispatch (not
// save) also covers playbooks saved before the ceiling existed.

var errStopAfterCapture = errors.New("captured; stop before the send path")

type clampCapturingCommandService struct {
	services.NodeCommandService
	slug    string
	payload map[string]interface{}
}

func (c *clampCapturingCommandService) RegisterNodeCommandWithWorkflowNode(_ context.Context, _ uuid.UUID, slug string, payload map[string]interface{}, _ interface{}, _ *uuid.UUID) (*node_command.NodeCommand, error) {
	c.slug, c.payload = slug, payload
	return nil, errStopAfterCapture
}

type clampConnectedComm struct {
	service.NodeCommunicationService
}

func (clampConnectedComm) IsNodeConnected(string) bool { return true }

func clampService(capture *clampCapturingCommandService) (*Service, *node.Node) {
	n := &node.Node{
		ID:             uuid.New(),
		AccountID:      uuid.New(),
		Status:         node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved,
		AgentVersion:   "0.5.0",
	}
	s := &Service{
		nodeRepo:             &payloadBoundRepo{n: n},
		communicationService: clampConnectedComm{},
		nodeCommandService:   capture,
		logger:               slog.Default(),
	}
	return s, n
}

func TestReadFileMaxBytesIsClampedAtDispatch(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)

	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugReadFile,
		map[string]interface{}{"path": "/var/log/syslog", "max_bytes": float64(2 << 30)},
		payloadBoundAuth{accountID: n.AccountID}, nil, nil, "")
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("expected the capture sentinel, got %v", err)
	}

	got, ok := capture.payload["max_bytes"].(float64)
	if !ok {
		t.Fatalf("max_bytes missing from payload: %+v", capture.payload)
	}
	if got != float64(node_file.MaxContentBytes) {
		t.Fatalf("max_bytes = %v, want clamped to %d", got, node_file.MaxContentBytes)
	}
}

func TestReadFileMaxBytesWithinCeilingPassesThrough(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)

	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugReadFile,
		map[string]interface{}{"path": "/var/log/syslog", "max_bytes": float64(4096)},
		payloadBoundAuth{accountID: n.AccountID}, nil, nil, "")
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("expected the capture sentinel, got %v", err)
	}

	if got := capture.payload["max_bytes"].(float64); got != 4096 {
		t.Fatalf("an in-bounds max_bytes must pass through unchanged, got %v", got)
	}
}
