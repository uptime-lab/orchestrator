package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// watchdogFakeRepo stubs the load/save pair the watchdog record methods touch;
// the embedded nil interface panics on anything else.
type watchdogFakeRepo struct {
	repository.NodeRepository
	node    *node.Node
	updates int
}

func (f *watchdogFakeRepo) GetByID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*node.Node, error) {
	return f.node, nil
}

func (f *watchdogFakeRepo) Update(_ context.Context, n *node.Node) error {
	f.updates++
	f.node = n
	return nil
}

// UpdateNodeColumns mirrors the real repository: NAMED columns, not a whole row.
// It rejects a column it does not know, because a typo in a column name is
// otherwise silent in the map form and this is the only place that can catch it.
func (f *watchdogFakeRepo) UpdateNodeColumns(_ context.Context, _, _ uuid.UUID, columns map[string]interface{}) error {
	f.updates++
	if f.node == nil {
		return errors.New("not found")
	}
	for name, value := range columns {
		text, _ := value.(string)
		switch name {
		case "watchdog_applied_hash":
			f.node.WatchdogAppliedHash = text
		case "watchdog_disarm_reason":
			f.node.WatchdogDisarmReason = text
		case "watchdog_last_refusal":
			f.node.WatchdogLastRefusal = text
		default:
			return fmt.Errorf("unexpected column %q", name)
		}
	}
	return nil
}

// watchdogFakeCmdSvc serves a fixed newest-first command page.
type watchdogFakeCmdSvc struct {
	services.NodeCommandService
	cmds []node_command.NodeCommand
}

func (f *watchdogFakeCmdSvc) GetNodeCommandsByNodeID(_ context.Context, _, _ uuid.UUID, _, _ int) ([]node_command.NodeCommand, error) {
	return f.cmds, nil
}

func watchdogService(repo *watchdogFakeRepo, cmdSvc services.NodeCommandService) *Service {
	return &Service{nodeRepo: repo, nodeCommandService: cmdSvc, logger: slog.Default()}
}

func TestRecordWatchdogRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("code and details are joined", func(t *testing.T) {
		repo := &watchdogFakeRepo{node: &node.Node{ID: uuid.New(), AccountID: uuid.New()}}
		svc := watchdogService(repo, nil)
		if err := svc.RecordWatchdogRefused(ctx, repo.node.AccountID, repo.node.ID, "ping_target_unreachable", "10.0.0.1 did not answer"); err != nil {
			t.Fatalf("RecordWatchdogRefused: %v", err)
		}
		if got, want := repo.node.WatchdogLastRefusal, "ping_target_unreachable: 10.0.0.1 did not answer"; got != want {
			t.Errorf("stored refusal %q, want %q", got, want)
		}
		if repo.updates != 1 {
			t.Errorf("expected one persist, got %d", repo.updates)
		}
	})

	t.Run("empty details leave just the code", func(t *testing.T) {
		repo := &watchdogFakeRepo{node: &node.Node{ID: uuid.New(), AccountID: uuid.New()}}
		svc := watchdogService(repo, nil)
		if err := svc.RecordWatchdogRefused(ctx, repo.node.AccountID, repo.node.ID, "no_thermal_sensor", ""); err != nil {
			t.Fatalf("RecordWatchdogRefused: %v", err)
		}
		if got := repo.node.WatchdogLastRefusal; got != "no_thermal_sensor" {
			t.Errorf("stored refusal %q, want the bare code", got)
		}
	})
}

// A confirmed apply supersedes both a prior breaker disarm and a prior
// refusal: what the device is running now is exactly what was asked for.
func TestRecordWatchdogAppliedClearsDisarmAndRefusal(t *testing.T) {
	repo := &watchdogFakeRepo{node: &node.Node{
		ID:                   uuid.New(),
		AccountID:            uuid.New(),
		WatchdogDisarmReason: "restart_loop",
		WatchdogLastRefusal:  "ping_target_unreachable: gone",
	}}
	svc := watchdogService(repo, nil)

	if err := svc.RecordWatchdogApplied(context.Background(), repo.node.AccountID, repo.node.ID, "abc123"); err != nil {
		t.Fatalf("RecordWatchdogApplied: %v", err)
	}
	if repo.node.WatchdogAppliedHash != "abc123" {
		t.Errorf("applied hash not stored: %q", repo.node.WatchdogAppliedHash)
	}
	if repo.node.WatchdogDisarmReason != "" {
		t.Errorf("disarm reason not cleared: %q", repo.node.WatchdogDisarmReason)
	}
	if repo.node.WatchdogLastRefusal != "" {
		t.Errorf("refusal not cleared: %q", repo.node.WatchdogLastRefusal)
	}
}

func TestRecordWatchdogDisarmedClearsAppliedHash(t *testing.T) {
	repo := &watchdogFakeRepo{node: &node.Node{
		ID:                  uuid.New(),
		AccountID:           uuid.New(),
		WatchdogAppliedHash: "abc123",
	}}
	svc := watchdogService(repo, nil)

	if err := svc.RecordWatchdogDisarmed(context.Background(), repo.node.AccountID, repo.node.ID, "restart_loop"); err != nil {
		t.Fatalf("RecordWatchdogDisarmed: %v", err)
	}
	if repo.node.WatchdogDisarmReason != "restart_loop" {
		t.Errorf("disarm reason not stored: %q", repo.node.WatchdogDisarmReason)
	}
	if repo.node.WatchdogAppliedHash != "" {
		t.Errorf("applied hash must be cleared on a disarm, got %q", repo.node.WatchdogAppliedHash)
	}
}

// GetWatchdog projects the newest apply_watchdog_config command into the state:
// still-registered means honestly "queued, not applied"; anything past that is
// on the device.
func TestGetWatchdogProjectsTheConvergeCommand(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	cfg := node.WatchdogConfig{Enabled: true, AgentHealth: true, AgentChangeSec: 300, IntervalSec: 10, RetryTimeoutSec: 60}

	newNode := func() *node.Node {
		return &node.Node{ID: uuid.New(), AccountID: accountID, WatchdogConfig: &cfg, WatchdogLastRefusal: "code: detail"}
	}
	wdCmd := func(status node_command.NodeCommandStatus, details string) node_command.NodeCommand {
		return node_command.NodeCommand{ID: uuid.New(), Slug: node_command.SlugApplyWatchdogConfig, Status: status, Details: details}
	}

	t.Run("registered command reports queued", func(t *testing.T) {
		repo := &watchdogFakeRepo{node: newNode()}
		cmds := &watchdogFakeCmdSvc{cmds: []node_command.NodeCommand{
			wdCmd(node_command.StatusRegistered, ""),
			wdCmd(node_command.StatusSuccess, ""), // older
		}}
		state, err := watchdogService(repo, cmds).GetWatchdog(ctx, accountID, repo.node.ID)
		if err != nil {
			t.Fatalf("GetWatchdog: %v", err)
		}
		if !state.Queued {
			t.Error("a still-registered converge must report queued=true")
		}
		if state.LastCommand == nil || state.LastCommand.Status != node_command.StatusRegistered.String() {
			t.Errorf("last_command = %+v, want the newest registered command", state.LastCommand)
		}
		if state.LastRefusal != "code: detail" {
			t.Errorf("last_refusal = %q, want the node's stored refusal", state.LastRefusal)
		}
	})

	t.Run("delivered command is not queued", func(t *testing.T) {
		repo := &watchdogFakeRepo{node: newNode()}
		cmds := &watchdogFakeCmdSvc{cmds: []node_command.NodeCommand{
			{ID: uuid.New(), Slug: node_command.SlugReboot, Status: node_command.StatusSuccess}, // other slugs are skipped
			wdCmd(node_command.StatusFailed, "would_lock_out"),
		}}
		state, err := watchdogService(repo, cmds).GetWatchdog(ctx, accountID, repo.node.ID)
		if err != nil {
			t.Fatalf("GetWatchdog: %v", err)
		}
		if state.Queued {
			t.Error("a terminal converge must not report queued")
		}
		if state.LastCommand == nil || state.LastCommand.Status != node_command.StatusFailed.String() || state.LastCommand.Details != "would_lock_out" {
			t.Errorf("last_command = %+v, want the failed converge with its details", state.LastCommand)
		}
	})

	t.Run("no converge command yet", func(t *testing.T) {
		repo := &watchdogFakeRepo{node: newNode()}
		state, err := watchdogService(repo, &watchdogFakeCmdSvc{}).GetWatchdog(ctx, accountID, repo.node.ID)
		if err != nil {
			t.Fatalf("GetWatchdog: %v", err)
		}
		if state.Queued || state.LastCommand != nil {
			t.Errorf("expected no projection, got queued=%v last_command=%+v", state.Queued, state.LastCommand)
		}
	})
}
