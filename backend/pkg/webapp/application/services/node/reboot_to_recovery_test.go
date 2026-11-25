package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

func recoveryCandidate(status node.NodeStatus, current string, order []string) *node.Node {
	on := true
	return &node.Node{
		ID: uuid.New(), AccountID: uuid.New(), Status: status, BootToOS: &on,
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			CurrentBootDevice: current, BootDevicesOrder: order,
		}}},
	}
}

func assertBootTargetUntouched(t *testing.T, repo *forceNetbootFakeRepo) {
	t.Helper()
	if repo.updated != nil {
		t.Fatalf("the boot target was written although the request was refused")
	}
	n := repo.stored
	if n.BootToOS == nil || !*n.BootToOS || n.RecoveryRevertPending || n.RecoveryPrevBootToOS != "" {
		t.Fatalf("boot state changed: boot_to_os=%v revert_pending=%v prev=%q",
			n.BootToOS, n.RecoveryRevertPending, n.RecoveryPrevBootToOS)
	}
}

// The one-shot pins boot_to_os to netboot and is undone only by the device
// reporting a network boot. A request that cannot end in one must be refused
// before anything is pinned, with a status that says "your node's state", not
// "the platform broke".
func TestRebootToRecoveryRefusesBeforeChangingState(t *testing.T) {
	ctx := context.Background()

	t.Run("an offline node", func(t *testing.T) {
		repo := &forceNetbootFakeRepo{stored: recoveryCandidate(node.StatusOffline, "nvme", []string{"http", "nvme"})}
		err := guardService(repo).RebootToRecovery(ctx, repo.stored.AccountID, uuid.New(), repo.stored.ID)
		var refused *RecoveryRebootRefusedError
		if !errors.As(err, &refused) || refused.ErrorCode() != "node_unavailable" || refused.HTTPStatus() != 409 {
			t.Fatalf("got %v, want a 409 node_unavailable refusal", err)
		}
		assertBootTargetUntouched(t, repo)
	})

	t.Run("a boot order that never reaches http", func(t *testing.T) {
		repo := &forceNetbootFakeRepo{stored: recoveryCandidate(node.StatusOnline, "nvme", []string{"nvme", "sd", "restart"})}
		err := guardService(repo).RebootToRecovery(ctx, repo.stored.AccountID, uuid.New(), repo.stored.ID)
		var refused *RecoveryRebootRefusedError
		if !errors.As(err, &refused) || refused.ErrorCode() != "recovery_unreachable" {
			t.Fatalf("got %v, want a recovery_unreachable refusal", err)
		}
		assertBootTargetUntouched(t, repo)
	})

	t.Run("an unreported boot order", func(t *testing.T) {
		repo := &forceNetbootFakeRepo{stored: recoveryCandidate(node.StatusOnline, "nvme", nil)}
		err := guardService(repo).RebootToRecovery(ctx, repo.stored.AccountID, uuid.New(), repo.stored.ID)
		var refused *RecoveryRebootRefusedError
		if !errors.As(err, &refused) || refused.ErrorCode() != "recovery_unreachable" {
			t.Fatalf("got %v, want a recovery_unreachable refusal", err)
		}
		assertBootTargetUntouched(t, repo)
	})
}

// When the reboot itself cannot be dispatched after the pin was written, the
// pin is put back exactly as it was: nothing will ever complete the one-shot.
func TestRebootToRecoveryRestoresTheBootTargetWhenTheRebootIsNotSent(t *testing.T) {
	ctx := context.Background()
	// A watcher node passes the boot checks and is refused at dispatch.
	stored := recoveryCandidate(node.StatusOnline, "nvme", []string{"http", "nvme"})
	stored.AgentKind = node.AgentKindWatcher
	repo := &forceNetbootFakeRepo{stored: stored}

	err := guardService(repo).RebootToRecovery(ctx, stored.AccountID, uuid.New(), stored.ID)
	var watcher *WatcherNodeError
	if !errors.As(err, &watcher) {
		t.Fatalf("got %v, want the dispatch refusal", err)
	}
	if stored.BootToOS == nil || !*stored.BootToOS || stored.RecoveryRevertPending || stored.RecoveryPrevBootToOS != "" {
		t.Fatalf("boot state not restored: boot_to_os=%v revert_pending=%v prev=%q",
			stored.BootToOS, stored.RecoveryRevertPending, stored.RecoveryPrevBootToOS)
	}
}

type activeDeviceCommandFake struct {
	services.NodeCommandService
	active *node_command.NodeCommand
	err    error
}

func (f activeDeviceCommandFake) FindActiveDeviceCommand(context.Context, uuid.UUID) (*node_command.NodeCommand, error) {
	return f.active, f.err
}

// While an install_os has not settled, the disk it writes is not a boot
// target; any other device command (or none) leaves boot_to_os in charge.
func TestOSInstallInFlight(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		active *node_command.NodeCommand
		want   bool
	}{
		{"an unsettled install", &node_command.NodeCommand{Slug: node_command.SlugInstallOS}, true},
		{"another device command", &node_command.NodeCommand{Slug: node_command.SlugEraseDrive}, false},
		{"nothing active", nil, false},
	}
	for _, c := range cases {
		s := &Service{nodeCommandService: activeDeviceCommandFake{active: c.active}}
		got, err := s.OSInstallInFlight(ctx, uuid.New())
		if err != nil || got != c.want {
			t.Errorf("%s: got (%v, %v), want %v", c.name, got, err, c.want)
		}
	}
	s := &Service{nodeCommandService: activeDeviceCommandFake{err: errors.New("db down")}}
	if _, err := s.OSInstallInFlight(ctx, uuid.New()); err == nil {
		t.Error("a lookup failure must be reported, not read as 'no install'")
	}
}

// A node whose bootloader fetches from another install is refused by the
// service itself, so the playbook block that calls it is refused like the
// HTTP route; a node pointing at this install, or one that reports no host,
// is not.
func TestRebootToRecoveryRefusesANodeBootingFromAnotherInstall(t *testing.T) {
	ctx := context.Background()
	candidate := func(host string) *forceNetbootFakeRepo {
		n := recoveryCandidate(node.StatusOnline, "nvme", []string{"http", "nvme"})
		n.SystemInfo.Hardware.Boot.BootHTTPHost = host
		return &forceNetbootFakeRepo{stored: n}
	}

	repo := candidate("boot.other.example")
	svc := guardService(repo)
	svc.SetBootHost("boot.example")
	err := svc.RebootToRecovery(ctx, repo.stored.AccountID, uuid.New(), repo.stored.ID)
	var refused *RecoveryRebootRefusedError
	if !errors.As(err, &refused) || refused.ErrorCode() != "recovery_unreachable" {
		t.Fatalf("got %v, want a recovery_unreachable refusal", err)
	}
	if strings.Contains(err.Error(), "boot.other.example") {
		t.Error("the refusal names the other install's host")
	}
	assertBootTargetUntouched(t, repo)

	for _, host := range []string{"BOOT.example", ""} {
		repo := candidate(host)
		svc := guardService(repo)
		svc.SetBootHost("boot.example")
		err := svc.RebootToRecovery(ctx, repo.stored.AccountID, uuid.New(), repo.stored.ID)
		if errors.As(err, &refused) {
			t.Fatalf("host %q refused: %v", host, err)
		}
	}
}
