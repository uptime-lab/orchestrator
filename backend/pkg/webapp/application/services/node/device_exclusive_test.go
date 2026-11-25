package node

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// exclusiveFakeNodeRepo returns one online node; anything else panics through
// the embedded nil interface.
type exclusiveFakeNodeRepo struct {
	repository.NodeRepository
	n *node.Node
}

func (f *exclusiveFakeNodeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return f.n, nil
}

// exclusiveFakeCmdService answers only the in-flight-device-command query.
type exclusiveFakeCmdService struct {
	services.NodeCommandService
	active *node_command.NodeCommand
	err    error
	calls  int
}

func (f *exclusiveFakeCmdService) FindActiveDeviceCommand(_ context.Context, _ uuid.UUID) (*node_command.NodeCommand, error) {
	f.calls++
	return f.active, f.err
}

type exclusiveAuthCtx struct{ accountID uuid.UUID }

func (a exclusiveAuthCtx) GetAccountID() uuid.UUID { return a.accountID }

func exclusiveService(cmdSvc services.NodeCommandService, n *node.Node) *Service {
	return &Service{
		nodeRepo:           &exclusiveFakeNodeRepo{n: n},
		nodeCommandService: cmdSvc,
		logger:             slog.Default(),
	}
}

func onlineNode(accountID uuid.UUID) *node.Node {
	return &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		Status:         node.StatusOnline,
		AgentVersion:   "9.9.9",
		ApprovalStatus: node.ApprovalStatusApproved,
	}
}

// The window this closes is the whole download: two install_os runs can hold the
// same block device open O_WRONLY and interleave writes. Without the guard only
// timing stops it.
func TestSecondDeviceCommandIsRefused(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := onlineNode(account)
	auth := exclusiveAuthCtx{accountID: account}

	activeID := uuid.New()
	cmdSvc := &exclusiveFakeCmdService{active: &node_command.NodeCommand{
		ID:     activeID,
		NodeID: n.ID,
		Slug:   node_command.SlugInstallOS,
		Status: node_command.StatusInProgress,
	}}
	s := exclusiveService(cmdSvc, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallOS,
		map[string]interface{}{"image_id": uuid.New().String(), "target_device": "/dev/nvme0n1"},
		auth, nil, nil, "")

	var busy *DeviceBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want a DeviceBusyError", err)
	}
	if busy.ActiveID != activeID {
		t.Errorf("ActiveID = %s, want the in-flight command %s — the operator's next step is to look at it", busy.ActiveID, activeID)
	}
	if !strings.Contains(busy.Error(), activeID.String()) {
		t.Errorf("message does not carry the in-flight command id: %s", busy.Error())
	}
}

// The set is exclusive as a GROUP: erase_drive racing install_os is exactly as
// destructive as two installs, so same-slug matching would miss it.
func TestDeviceExclusivityIsCrossSlug(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := onlineNode(account)
	auth := exclusiveAuthCtx{accountID: account}

	cmdSvc := &exclusiveFakeCmdService{active: &node_command.NodeCommand{
		ID:     uuid.New(),
		NodeID: n.ID,
		Slug:   node_command.SlugEraseDrive,
		Status: node_command.StatusInProgress,
	}}
	s := exclusiveService(cmdSvc, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallOS,
		map[string]interface{}{"image_id": uuid.New().String(), "target_device": "/dev/nvme0n1"},
		auth, nil, nil, "")

	var busy *DeviceBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("install_os was allowed while erase_drive holds the disk: %v", err)
	}
	if busy.ActiveCmd != node_command.SlugEraseDrive {
		t.Errorf("ActiveCmd = %q, want erase_drive named so the message is actionable", busy.ActiveCmd)
	}
}

// A node-wide lock was explicitly rejected: commands are accepted in maintenance
// on purpose (it is how a stuck node is recovered), and the reboot and
// install_agent that follow an install must not be blocked by it.
func TestNonDeviceCommandsAreNotBlocked(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := onlineNode(account)
	auth := exclusiveAuthCtx{accountID: account}

	cmdSvc := &exclusiveFakeCmdService{active: &node_command.NodeCommand{
		ID: uuid.New(), NodeID: n.ID, Slug: node_command.SlugInstallOS, Status: node_command.StatusInProgress,
	}}
	s := exclusiveService(cmdSvc, n)

	// reboot is not device-exclusive, so the guard must not even ask.
	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugReboot, nil, auth, nil, nil, "")

	var busy *DeviceBusyError
	if errors.As(err, &busy) {
		t.Fatal("reboot was refused because a device command is running; that is the node-wide lock this avoids")
	}
	if cmdSvc.calls != 0 {
		t.Errorf("the guard queried the repository for a non-device command (%d calls)", cmdSvc.calls)
	}
}

// A database blip must not make the fleet un-installable — the same fail-open
// rule the per-tenant count caps follow.
func TestGuardFailsOpenOnQueryError(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := onlineNode(account)
	auth := exclusiveAuthCtx{accountID: account}

	cmdSvc := &exclusiveFakeCmdService{err: errors.New("connection reset")}
	s := exclusiveService(cmdSvc, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallOS,
		map[string]interface{}{"image_id": uuid.New().String(), "target_device": "/dev/nvme0n1"},
		auth, nil, nil, "")

	var busy *DeviceBusyError
	if errors.As(err, &busy) {
		t.Fatal("a failed lookup refused the command; the guard must fail open")
	}
	if cmdSvc.calls != 1 {
		t.Errorf("query calls = %d, want 1", cmdSvc.calls)
	}
}

// The domain set is the single source of truth for both the guard and the SQL
// query, so it must name exactly the slugs that own a disk.
func TestDeviceExclusiveSet(t *testing.T) {
	for _, slug := range []string{
		node_command.SlugInstallOS, node_command.SlugEraseDrive, node_command.SlugCaptureImage,
		// The recovery-mode access apply mounts and writes the target disk, so
		// it must not race an install/erase/capture on the same device.
		node_command.SlugApplyAccessConfigOffline,
		// The offline patch mounts a partition of the target disk for the same
		// reason and must not race the same writers.
		node_command.SlugPatchFileOffline,
	} {
		if !node_command.IsDeviceExclusive(slug) {
			t.Errorf("%s owns a block device but is not in the exclusive set", slug)
		}
	}
	for _, slug := range []string{
		node_command.SlugReboot, node_command.SlugInstallAgent, node_command.SlugStatusCheck,
		node_command.SlugApplyAccessConfig, "run_script",
	} {
		if node_command.IsDeviceExclusive(slug) {
			t.Errorf("%s does not own a disk; blocking it recreates the node-wide lock", slug)
		}
	}
	if got := len(node_command.DeviceExclusiveSlugs()); got != 5 {
		t.Errorf("DeviceExclusiveSlugs() has %d entries, want 5 — the query and the guard must agree", got)
	}
}
