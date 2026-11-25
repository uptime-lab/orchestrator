package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

func overlayNode(accountID uuid.UUID, persistence string) *node.Node {
	n := &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		Status:         node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved,
		AgentVersion:   "0.7.0",
	}
	if persistence != "" {
		n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{RootPersistence: persistence}}}
	}
	return n
}

// A write whose point is surviving a reboot is refused on an overlay root —
// BEFORE registration or dispatch, like the payload-bound guard: the
// deliberately-nil nodeCommandService would panic if the gate ran late.
func TestOverlayRootRefusesPersistentWrites(t *testing.T) {
	accountID := uuid.New()
	n := overlayNode(accountID, node.RootPersistenceOverlay)
	s := &Service{
		nodeRepo:             &payloadBoundRepo{n: n},
		communicationService: payloadBoundComm{},
		logger:               slog.Default(),
	}

	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugApplyAccessConfig,
		map[string]interface{}{}, payloadBoundAuth{accountID: accountID}, nil, nil, "")

	var overlay *OverlayRootError
	if !errors.As(err, &overlay) {
		t.Fatalf("expected OverlayRootError, got %v", err)
	}
	if overlay.HTTPStatus() != 409 || overlay.ErrorCode() != "overlay_root" {
		t.Errorf("status/code = %d/%s, want 409/overlay_root", overlay.HTTPStatus(), overlay.ErrorCode())
	}
}

// "" must change NOTHING: an agent below the reporting floor keeps today's
// behaviour. The command must get PAST the overlay gate and fail later on the
// unconnected node — never as an OverlayRootError.
func TestUnknownPersistenceChangesNothing(t *testing.T) {
	accountID := uuid.New()
	n := overlayNode(accountID, "")
	s := &Service{
		nodeRepo:             &payloadBoundRepo{n: n},
		communicationService: payloadBoundComm{},
		logger:               slog.Default(),
	}

	// patch_file with an oversized field: the payload-size guard sits AFTER
	// the overlay gate, so reaching IT proves the gate let the command pass.
	oversized := map[string]interface{}{"content": string(make([]byte, 600000)), "path": "/boot/firmware/config.txt"}
	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugPatchFile,
		oversized, payloadBoundAuth{accountID: accountID}, nil, nil, "")

	var overlay *OverlayRootError
	if errors.As(err, &overlay) {
		t.Fatal("an unreported persistence fact must not refuse — old agents keep working")
	}
	var tooLarge *CommandPayloadTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected to reach the payload-size guard past the overlay gate, got %v", err)
	}
}

// Reads and reboots stay allowed on an overlay root: the refusal list is the
// write-shaped family, not "everything".
func TestOverlayRootStillAllowsEphemeralCommands(t *testing.T) {
	for _, slug := range []string{node_command.SlugReboot, node_command.SlugRunScript, node_command.SlugReadFile} {
		if node_command.RequiresPersistentRoot(slug) {
			t.Errorf("%s must not require a persistent root", slug)
		}
	}
	for _, slug := range []string{node_command.SlugApplyAccessConfig, node_command.SlugUpdateAgent, node_command.SlugSetHostname} {
		if !node_command.RequiresPersistentRoot(slug) {
			t.Errorf("%s must require a persistent root", slug)
		}
	}
}
