package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	node_command "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// installHistoryFake answers only the install-history question; the embedded
// nil interface panics on anything else.
type installHistoryFake struct {
	services.NodeCommandService
	installed bool
	// erasedSince: an install succeeded, and an erase_drive succeeded after it.
	erasedSince bool
	err         error
	asked       []string
}

func (f *installHistoryFake) LatestSucceededCommand(_ context.Context, _, _ uuid.UUID, slugs []string) (string, error) {
	f.asked = append(f.asked, strings.Join(slugs, ","))
	switch {
	case f.erasedSince:
		return node_command.SlugEraseDrive, f.err
	case f.installed:
		return node_command.SlugInstallOS, f.err
	}
	return "", f.err
}

func bootReportingNode(accountID uuid.UUID, device string, recovery bool) *node.Node {
	return &node.Node{
		ID: uuid.New(), AccountID: accountID,
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			CurrentBootDevice: device, RecoveryBootstrap: recovery,
		}}},
	}
}

// The post-provision default points the node at its installed OS. A node in
// the recovery system with no OS install on record has nothing to point at:
// "on" withholds recovery from it, and the ROM falls through to an empty disk
// or to whatever an earlier owner left there.
func TestDefaultBootToOSOnNeedsAnInstalledOS(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()

	run := func(t *testing.T, n *node.Node, history *installHistoryFake) (*forceNetbootFakeRepo, error) {
		t.Helper()
		repo := &forceNetbootFakeRepo{stored: n}
		svc := guardService(repo)
		if history != nil {
			svc.nodeCommandService = history
		}
		return repo, svc.DefaultBootToOSOn(ctx, n.ID, accountID)
	}

	t.Run("recovery from a provisioning card, nothing installed: left to inherit", func(t *testing.T) {
		history := &installHistoryFake{}
		repo, err := run(t, bootReportingNode(accountID, "sd", true), history)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS != nil {
			t.Fatalf("boot_to_os = %v, want inherit: the node has no OS to boot", *repo.stored.BootToOS)
		}
		if len(history.asked) != 1 || history.asked[0] != node_command.SlugInstallOS+","+node_command.SlugEraseDrive {
			t.Fatalf("asked %v, want one lookup of the latest disk write", history.asked)
		}
	})

	t.Run("netbooted recovery, nothing installed: left to inherit", func(t *testing.T) {
		repo, err := run(t, bootReportingNode(accountID, "http", false), &installHistoryFake{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS != nil {
			t.Fatalf("boot_to_os = %v, want inherit", *repo.stored.BootToOS)
		}
	})

	t.Run("recovery after a successful install: defaults on", func(t *testing.T) {
		repo, err := run(t, bootReportingNode(accountID, "sd", true), &installHistoryFake{installed: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS == nil || !*repo.stored.BootToOS {
			t.Fatalf("boot_to_os = %v, want on", repo.stored.BootToOS)
		}
	})

	t.Run("recovery after an install and a later erase: left to inherit", func(t *testing.T) {
		repo, err := run(t, bootReportingNode(accountID, "http", true), &installHistoryFake{erasedSince: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS != nil {
			t.Fatalf("boot_to_os = %v, want inherit: the disk was erased after the install", *repo.stored.BootToOS)
		}
	})

	t.Run("running an installed OS: defaults on without consulting history", func(t *testing.T) {
		history := &installHistoryFake{}
		repo, err := run(t, bootReportingNode(accountID, "nvme", false), history)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS == nil || !*repo.stored.BootToOS {
			t.Fatalf("boot_to_os = %v, want on", repo.stored.BootToOS)
		}
		if len(history.asked) != 0 {
			t.Fatalf("consulted install history for a node on its own OS: %v", history.asked)
		}
	})

	t.Run("history unreadable: left to inherit and reported", func(t *testing.T) {
		repo, err := run(t, bootReportingNode(accountID, "sd", true), &installHistoryFake{err: errors.New("db down")})
		if err == nil {
			t.Fatal("want the lookup failure reported")
		}
		if repo.stored.BootToOS != nil {
			t.Fatalf("boot_to_os = %v, want inherit", *repo.stored.BootToOS)
		}
	})

	t.Run("an explicit choice is never touched", func(t *testing.T) {
		n := bootReportingNode(accountID, "nvme", false)
		off := false
		n.BootToOS = &off
		repo, err := run(t, n, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.stored.BootToOS == nil || *repo.stored.BootToOS {
			t.Fatalf("boot_to_os = %v, want the explicit off kept", repo.stored.BootToOS)
		}
	})
}
