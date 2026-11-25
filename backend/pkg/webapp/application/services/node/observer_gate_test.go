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

// A watcher node executes no commands, so the single dispatch choke point
// must refuse EVERY slug — imperative, queueable, privileged, all of them —
// with the typed error that names the remedy.
func TestWatcherNodeRefusesAllCommands(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	for _, slug := range []string{
		node_command.SlugReboot,
		node_command.SlugStatusCheck,
		node_command.SlugApplyAccessConfig, // queueable does not exempt it
		node_command.SlugInstallOS,
		node_command.SlugRunScript,
	} {
		n := onlineNode(account)
		n.AgentKind = node.AgentKindWatcher
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, slug, map[string]interface{}{}, auth, nil, nil, "")
		var watcher *WatcherNodeError
		if !errors.As(err, &watcher) {
			t.Errorf("%s: err = %v, want WatcherNodeError", slug, err)
			continue
		}
		if !strings.Contains(watcher.Error(), "recovery") {
			t.Errorf("%s: the refusal must name the remedy (recovery): %s", slug, watcher.Error())
		}
	}
}

// The one exception: the recovery-mode access apply. In recovery the machine
// runs the FULL recovery agent, and this path is exactly how a watcher node
// is supposed to be managed — refusing it would close the loop the feature
// exists for.
func TestWatcherNodeAllowsOfflineAccessApply(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	n := recoveryNode(account)
	n.AgentKind = node.AgentKindWatcher
	auth := exclusiveAuthCtx{accountID: account}
	s := exclusiveService(&exclusiveFakeCmdService{}, n)

	_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugApplyAccessConfigOffline,
		map[string]interface{}{"target_device": "/dev/nvme0n1"}, auth, nil, nil, "")
	var watcher *WatcherNodeError
	if errors.As(err, &watcher) {
		t.Fatalf("apply_access_config_offline must not be blocked by the watcher gate: %v", err)
	}
}

// A full-agent node is untouched by the gate, and an empty kind (an agent
// that predates the field) counts as full.
func TestFullAndLegacyKindsAreNotGated(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()
	auth := exclusiveAuthCtx{accountID: account}

	for _, kind := range []string{"", node.AgentKindFull} {
		n := onlineNode(account)
		n.AgentKind = kind
		s := exclusiveService(&exclusiveFakeCmdService{}, n)
		_, _, err := s.sendCommandToNode(ctx, n.ID, node_command.SlugReboot, map[string]interface{}{}, auth, nil, nil, "")
		var watcher *WatcherNodeError
		if errors.As(err, &watcher) {
			t.Errorf("kind %q must not be gated: %v", kind, err)
		}
	}
}
