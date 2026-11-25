package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// The watcher/Watcher install is refused against a recovery agent that
// predates the variant field: an older agent ignores the unknown proto field
// and silently installs the FULL agent — the opposite of what the operator
// chose. The refusal must be the typed AgentTooOldError naming the floor.
func TestInstallAgentWatcherVariantIsVersionGated(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	n := onlineNode(account)
	n.AgentVersion = "0.6.2" // below the watcher-install floor
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcher"},
		auth, nil, nil, "")
	var tooOld *AgentTooOldError
	if !errors.As(err, &tooOld) {
		t.Fatalf("err = %v, want AgentTooOldError", err)
	}
	if tooOld.Require != node_command.MinAgentVersionWatcherInstall {
		t.Errorf("Require = %s, want %s", tooOld.Require, node_command.MinAgentVersionWatcherInstall)
	}
}

// A full install (or no variant at all) keeps working on older recovery
// agents — the floor is conditional, not a new floor for install_agent.
func TestInstallAgentFullVariantHasNoNewFloor(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	for _, params := range []map[string]interface{}{
		{"target_device": "/dev/nvme0n1", "agent_version": "latest"},
		{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "full"},
	} {
		n := onlineNode(account)
		n.AgentVersion = "0.6.0"
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent, params, auth, nil, nil, "")
		var tooOld *AgentTooOldError
		if errors.As(err, &tooOld) {
			t.Errorf("params %v: full install must not be version-gated: %v", params, err)
		}
	}
}

// An unknown variant is refused loudly, never defaulted.
func TestInstallAgentUnknownVariantRefused(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}
	n := onlineNode(account)
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcherr"},
		auth, nil, nil, "")
	if err == nil || !strings.Contains(err.Error(), "unknown variant") {
		t.Fatalf("err = %v, want an unknown-variant refusal", err)
	}
}

// recoveryNodeWithBootOrder is a node netbooted into recovery, reporting the
// boot order its EEPROM will actually follow — the two facts the Watcher gate
// reads.
func recoveryNodeWithBootOrder(accountID uuid.UUID, order []string) *node.Node {
	n := onlineNode(accountID)
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
		CurrentBootDevice: "http",
		RecoveryBootstrap: true,
		BootDevicesOrder:  order,
	}}}
	return n
}

// The SAME recovery bootstrap runs from a PROVISIONING SD CARD, reporting
// recovery_bootstrap=true and current_boot_device="sd". That is no evidence at
// all that this device's EEPROM reaches this platform — the card was inserted
// by a human — so the gate must refuse it even though its boot order looks
// perfect. Keying the gate on the bootstrap flag alone passed this node.
func TestInstallAgentWatcherRefusedOnACardBootedBootstrap(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	n := onlineNode(account)
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
		CurrentBootDevice: "sd", // booted from the provisioning card
		RecoveryBootstrap: true,
		BootDevicesOrder:  node.NetworkFirstBootOrder,
	}}}
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcher"},
		auth, nil, nil, "")

	var unsafe *WatcherInstallUnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("err = %v, want WatcherInstallUnsafeError — a card boot is not a netboot", err)
	}
}

// Installing Watcher is a one-way door: the node executes no commands
// afterwards, so the only route back is a recovery boot the DEVICE starts. A
// boot order that reaches a local disk first means it never will — once the
// install puts a bootable OS on that disk, nvme wins every power cycle.
func TestInstallAgentWatcherRefusedWhenTheNodeCannotReturnToNetboot(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	for _, tc := range []struct {
		name  string
		order []string
	}{
		{"a local disk ahead of http", []string{"nvme", "http", "restart"}},
		{"no http entry at all", []string{"nvme", "sd"}},
		{"the device never reported an order", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := recoveryNodeWithBootOrder(account, tc.order)
			s := exclusiveService(&exclusiveFakeCmdService{}, n)

			_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
				map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcher"},
				auth, nil, nil, "")

			var unsafe *WatcherInstallUnsafeError
			if !errors.As(err, &unsafe) {
				t.Fatalf("err = %v, want WatcherInstallUnsafeError", err)
			}
			// The refusal has to say what to do about it, not just that it
			// happened — AND the remedy has to be performable. Re-baking the
			// EEPROM stages to SD/USB media only, so the message names that
			// precondition; without it the advice sends an NVMe-only board's
			// operator in a circle.
			if !strings.Contains(unsafe.Error(), "provision its EEPROM with a network-first boot order") {
				t.Errorf("refusal does not name the remedy: %v", unsafe)
			}
			if !strings.Contains(unsafe.Error(), "SD or USB boot media") {
				t.Errorf("refusal names a remedy without its precondition: %v", unsafe)
			}
		})
	}
}

// Being netbooted RIGHT NOW is the direct evidence that this device's EEPROM
// reaches this install at all. A node on its installed OS has not shown that,
// and the platform cannot make it show that afterwards.
func TestInstallAgentWatcherRefusedOnALiveOS(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	n := onlineNode(account)
	n.SystemInfo = &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
		CurrentBootDevice: "nvme",
		BootDevicesOrder:  node.NetworkFirstBootOrder, // order is fine; the node is not in recovery
	}}}
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcher"},
		auth, nil, nil, "")

	var unsafe *WatcherInstallUnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("err = %v, want WatcherInstallUnsafeError", err)
	}
	if !strings.Contains(unsafe.Reason, "did not netboot") {
		t.Errorf("reason = %q, want it to name the missing evidence", unsafe.Reason)
	}
}

// The gate is for the watcher variant ONLY. A full agent keeps every command,
// so a node that cannot netboot is still fully manageable — refusing that
// install would block the very fix (install the full agent) the refusal above
// recommends.
func TestInstallAgentFullVariantIsNotNetbootGated(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	n := recoveryNodeWithBootOrder(account, []string{"nvme", "sd"})
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "full"},
		auth, nil, nil, "")

	var unsafe *WatcherInstallUnsafeError
	if errors.As(err, &unsafe) {
		t.Fatalf("a full install was netboot-gated: %v", err)
	}
}

// A netboot-capable node in recovery passes the gate and reaches dispatch.
func TestInstallAgentWatcherAllowedOnANetbootCapableRecoveryNode(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	n := recoveryNodeWithBootOrder(account, node.NetworkFirstBootOrder)
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugInstallAgent,
		map[string]interface{}{"target_device": "/dev/nvme0n1", "agent_version": "latest", "variant": "watcher"},
		auth, nil, nil, "")

	// Asserting the call reached DISPATCH, not merely that two error types are
	// absent: the weaker form passes when the gate refuses for a third reason.
	// This fixture has no communication service, so getting past the gate has
	// exactly one signature, and it is not any of the gate's own refusals.
	var unsafe *WatcherInstallUnsafeError
	if errors.As(err, &unsafe) {
		t.Fatalf("a netboot-capable recovery node was refused: %v", err)
	}
	var tooOld *AgentTooOldError
	if errors.As(err, &tooOld) {
		t.Fatalf("unexpected version refusal: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "communication service not available") {
		t.Fatalf("err = %v, want the post-gate dispatch failure this fixture produces", err)
	}
}
