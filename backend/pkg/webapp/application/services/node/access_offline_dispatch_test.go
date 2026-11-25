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

// recoveryNode is an online node reporting the RAM-backed recovery bootstrap.
func recoveryNode(accountID uuid.UUID) *node.Node {
	n := onlineNode(accountID)
	n.SystemInfo = &node.SystemInfo{
		Hardware: &node.HardwareInfo{
			Boot: &node.BootInfo{RecoveryBootstrap: true, CurrentBootDevice: "http"},
		},
	}
	return n
}

// The offline apply mounts and rewrites the disk the machine would otherwise
// be running from, so a node on an installed OS must be refused at dispatch —
// the agent enforces the same gate on arrival, but the platform-side refusal
// is the one that can tell the operator what to do instead.
func TestOfflineAccessApplyRefusedOutsideRecovery(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := onlineNode(account) // no SystemInfo → not a recovery bootstrap
	auth := exclusiveAuthCtx{accountID: account}
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
		map[string]interface{}{"target_device": "/dev/nvme0n1"}, auth, nil, nil, "")
	if err == nil {
		t.Fatal("expected a refusal for a node not in recovery")
	}
	if !strings.Contains(err.Error(), "netbooted into recovery") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The request is well formed and the node's state refuses it: 409, at
	// every route that dispatches it, instead of the generic 500.
	var refusal *RecoveryRequiredError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "recovery_required" {
		t.Fatalf("err = %v, want a typed 409 recovery_required", err)
	}
}

// In recovery the netboot gate must pass; the dispatch then proceeds into the
// normal pipeline (whose later stages this harness does not wire, so any error
// past the gate must NOT be the netboot refusal).
func TestOfflineAccessApplyPassesGateInRecovery(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := recoveryNode(account)
	auth := exclusiveAuthCtx{accountID: account}
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
		map[string]interface{}{"target_device": "/dev/nvme0n1"}, auth, nil, nil, "")
	if err != nil && strings.Contains(err.Error(), "netbooted into recovery") {
		t.Fatalf("netboot gate refused a node that IS in recovery: %v", err)
	}
}

// An empty target_device resolves from the node's own reported inventory at
// DISPATCH (the workflow engine and the generic route land here with the
// params as authored; the dedicated HTTP route pre-resolves the same way).
// The auto-pick never guesses: no reported disk refuses, several refuse with
// the candidates named, exactly one is used.
func TestOfflineAccessApplyDispatchAutoPicksTheTargetDevice(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	t.Run("no devices reported refuses", func(t *testing.T) {
		n := recoveryNode(account)
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
			map[string]interface{}{}, auth, nil, nil, "")
		if err == nil || !strings.Contains(err.Error(), "storage devices") {
			t.Fatalf("expected a no-reported-disk refusal, got: %v", err)
		}
	})

	t.Run("two devices refuse rather than guess", func(t *testing.T) {
		n := recoveryNode(account)
		n.SystemInfo.StorageDevices = map[string]*node.StorageDevice{"/dev/nvme0n1": {}, "/dev/sda": {}}
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
			map[string]interface{}{}, auth, nil, nil, "")
		if err == nil || !strings.Contains(err.Error(), "/dev/nvme0n1, /dev/sda") {
			t.Fatalf("expected the candidates named, got: %v", err)
		}
	})

	t.Run("exactly one device is picked", func(t *testing.T) {
		n := recoveryNode(account)
		n.SystemInfo.StorageDevices = map[string]*node.StorageDevice{"/dev/nvme0n1": {}}
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		params := map[string]interface{}{}
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
			params, auth, nil, nil, "")
		if err != nil && (strings.Contains(err.Error(), "storage devices") || strings.Contains(err.Error(), "target_device")) {
			t.Fatalf("auto-pick failed with one device: %v", err)
		}
		if got, _ := params["target_device"].(string); got != "/dev/nvme0n1" {
			t.Fatalf("target_device = %q, want the sole reported disk", got)
		}
	})
}

// ApplyAccessConfigOffline's auto-pick: exactly one reported disk is used;
// several are refused with the candidates named; none is refused.
func TestOfflineAccessApplyAutoPick(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()

	cases := []struct {
		name    string
		devices map[string]*node.StorageDevice
		wantErr string
	}{
		{"no devices reported", nil, "has not reported any storage devices"},
		{"two devices is ambiguous", map[string]*node.StorageDevice{
			"/dev/nvme0n1": {}, "/dev/sda": {},
		}, "/dev/nvme0n1, /dev/sda"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := recoveryNode(account)
			n.SystemInfo.StorageDevices = tc.devices
			s := exclusiveService(&exclusiveFakeCmdService{}, n)
			err := s.ApplyAccessConfigOffline(ctx, account, n.ID, "", 0)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}

	t.Run("exactly one device is used", func(t *testing.T) {
		n := recoveryNode(account)
		n.SystemInfo.StorageDevices = map[string]*node.StorageDevice{"/dev/nvme0n1": {}}
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		err := s.ApplyAccessConfigOffline(ctx, account, n.ID, "", 0)
		// The pick must succeed; any error past it comes from the unwired tail
		// of the pipeline and must not be about the target device.
		if err != nil && (strings.Contains(err.Error(), "storage devices") || strings.Contains(err.Error(), "target_device")) {
			t.Fatalf("auto-pick failed with one device: %v", err)
		}
	})
}
