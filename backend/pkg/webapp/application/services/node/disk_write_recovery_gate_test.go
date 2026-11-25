package node

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// managedOSOnSD is an online node running its own operating system from an
// SD card, with a second disk the OS has not mounted.
func managedOSOnSD(accountID uuid.UUID) *node.Node {
	n := onlineNode(accountID)
	n.SystemInfo = &node.SystemInfo{
		Hardware: &node.HardwareInfo{Boot: &node.BootInfo{CurrentBootDevice: "sd", RootDevice: "/dev/mmcblk0"}},
		StorageDevices: map[string]*node.StorageDevice{
			"/dev/mmcblk0": {}, "/dev/nvme0n1": {},
		},
	}
	return n
}

// Overwriting a used disk is a recovery-system operation: from a running OS
// the device refuses only a disk that is mounted or busy, so an unmounted
// second disk holding data would be erased. The refusal is the platform's,
// so it holds whatever agent the node runs.
func TestInstallOSOverwriteRefusedOutsideRecovery(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := managedOSOnSD(account)
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallOS,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "wipe": true}, exclusiveAuthCtx{accountID: account}, nil, nil, "")
	var refusal *RecoveryRequiredError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "recovery_required" {
		t.Fatalf("err = %v, want a typed 409 recovery_required", err)
	}
}

// Installing onto a blank second disk from the running OS stays allowed: the
// device itself refuses a disk that already has partitions when overwrite is
// off. And from recovery an overwrite passes the gate (the node panel's path).
func TestInstallOSRecoveryGateLeavesTheAllowedPathsAlone(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	cases := []struct {
		name   string
		node   *node.Node
		params map[string]interface{}
	}{
		{"blank second disk from the running OS", managedOSOnSD(account), map[string]interface{}{"target_device": "/dev/nvme0n1", "wipe": false}},
		{"overwrite from recovery", recoveryNode(account), map[string]interface{}{"target_device": "/dev/nvme0n1", "wipe": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := exclusiveService(&exclusiveFakeCmdService{}, tc.node)
			_, _, err := s.sendCommandToNode(ctx, tc.node.ID, node_command.SlugInstallOS, tc.params, exclusiveAuthCtx{accountID: account}, nil, nil, "")
			var refusal *RecoveryRequiredError
			if errors.As(err, &refusal) {
				t.Fatalf("refused for the boot state: %v", err)
			}
		})
	}
}

// A capture with no target names a disk the server can stand behind: the
// node's only reported disk. Several disks are refused rather than guessed,
// because the device's own fallback is a fixed device name that is wrong on
// boards booting from eMMC, SD or USB, and silently the wrong disk on one
// that runs from SD beside an NVMe data disk.
func TestCaptureImageResolvesOrRefusesAnEmptyTarget(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	t.Run("several disks are refused with the candidates named", func(t *testing.T) {
		n := recoveryNode(account)
		n.SystemInfo.StorageDevices = map[string]*node.StorageDevice{"/dev/mmcblk0": {}, "/dev/nvme0n1": {}}
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugCaptureImage,
			map[string]interface{}{"title": "backup"}, auth, nil, nil, "")
		if err == nil || !strings.Contains(err.Error(), "/dev/mmcblk0, /dev/nvme0n1") {
			t.Fatalf("expected a refusal naming the disks, got: %v", err)
		}
	})

	t.Run("the only disk is named in the payload", func(t *testing.T) {
		n := recoveryNode(account)
		n.SystemInfo.StorageDevices = map[string]*node.StorageDevice{"/dev/mmcblk0": {}}
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		params := map[string]interface{}{"title": "backup"}
		_, _, _ = s.sendCommandToNode(ctx, n.ID, node_command.SlugCaptureImage, params, auth, nil, nil, "")
		if got, _ := params["target_device"].(string); got != "/dev/mmcblk0" {
			t.Fatalf("target_device = %q, want the sole reported disk", got)
		}
	})
}
