package node

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// cmdAt builds a history entry; offset orders CreatedAt (larger = newer).
func cmdAt(slug string, status node_command.NodeCommandStatus, offset time.Duration) node_command.NodeCommand {
	return node_command.NodeCommand{
		ID:        uuid.New(),
		Slug:      slug,
		Status:    status,
		CreatedAt: time.Unix(1_700_000_000, 0).Add(offset),
	}
}

// newestFirst mirrors the repository ordering (created_at DESC) the decision
// consumes.
func newestFirst(cmds ...node_command.NodeCommand) []node_command.NodeCommand {
	return cmds
}

func TestAccessReapplyNeeded(t *testing.T) {
	apply := node_command.SlugApplyAccessConfig
	install := node_command.SlugInstallOS

	cases := []struct {
		name   string
		recent []node_command.NodeCommand
		want   bool
	}{
		{"no history", nil, false},
		{"access never applied", newestFirst(
			cmdAt(node_command.SlugReboot, node_command.StatusSuccess, 2*time.Hour),
		), false},
		{"last apply failed (died in recovery)", newestFirst(
			cmdAt(apply, node_command.StatusFailed, 2*time.Hour),
		), true},
		{"last apply expired while parked in recovery", newestFirst(
			cmdAt(apply, node_command.StatusExpired, 2*time.Hour),
		), true},
		{"last apply skipped (agent too old at redelivery)", newestFirst(
			cmdAt(apply, node_command.StatusSkipped, 2*time.Hour),
		), true},
		{"last apply queued — redelivery owns it", newestFirst(
			cmdAt(apply, node_command.StatusRegistered, 2*time.Hour),
		), false},
		{"last apply in flight", newestFirst(
			cmdAt(apply, node_command.StatusInProgress, 2*time.Hour),
		), false},
		{"apply succeeded, no reinstall since", newestFirst(
			cmdAt(apply, node_command.StatusSuccess, 2*time.Hour),
			cmdAt(install, node_command.StatusSuccess, 1*time.Hour),
		), false},
		{"apply succeeded but a reinstall postdates it", newestFirst(
			cmdAt(install, node_command.StatusSuccess, 3*time.Hour),
			cmdAt(apply, node_command.StatusSuccess, 2*time.Hour),
		), true},
		{"failed reinstall does not count as a disk rewrite", newestFirst(
			cmdAt(install, node_command.StatusFailed, 3*time.Hour),
			cmdAt(apply, node_command.StatusSuccess, 2*time.Hour),
		), false},
		{"newest apply wins over an older failed one", newestFirst(
			cmdAt(apply, node_command.StatusSuccess, 3*time.Hour),
			cmdAt(apply, node_command.StatusFailed, 2*time.Hour),
		), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accessReapplyNeeded(tc.recent); got != tc.want {
				t.Errorf("accessReapplyNeeded() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiresManagedOS(t *testing.T) {
	if !node_command.RequiresManagedOS(node_command.SlugApplyAccessConfig) {
		t.Error("apply_access_config must require a managed OS")
	}
	for _, slug := range []string{node_command.SlugReboot, node_command.SlugSetHostname, node_command.SlugPatchFile, node_command.SlugRunScript} {
		if node_command.RequiresManagedOS(slug) {
			t.Errorf("%s must not require a managed OS", slug)
		}
	}
	// Every managed-OS-only slug must also be queueable: the guard holds the
	// command "registered", which only a queueable command may survive as.
	if !node_command.IsQueueable(node_command.SlugApplyAccessConfig) {
		t.Error("managed-OS-only commands must be queueable")
	}
}
