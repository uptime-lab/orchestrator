package grpc

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// watchdogFakeNodeService records the watchdog-fact calls the settle and the
// disarm routing make; the embedded nil interface panics on anything else,
// proving these paths touch nothing more.
type watchdogFakeNodeService struct {
	services.NodeService

	appliedHash   string
	appliedCalls  int
	refusal       string
	refusalDet    string
	refusedCalls  int
	disarmReason  string
	disarmedCalls int
	disarmedAcct  uuid.UUID
	disarmedNode  uuid.UUID
	renderedPath  string
	renderedText  string
	renderedCalls int
}

func (f *watchdogFakeNodeService) RecordWatchdogApplied(_ context.Context, _, _ uuid.UUID, appliedHash string) error {
	f.appliedCalls++
	f.appliedHash = appliedHash
	return nil
}

func (f *watchdogFakeNodeService) RecordWatchdogRefused(_ context.Context, _, _ uuid.UUID, refusal, details string) error {
	f.refusedCalls++
	f.refusal = refusal
	f.refusalDet = details
	return nil
}

func (f *watchdogFakeNodeService) RecordWatchdogDisarmed(_ context.Context, accountID, nodeID uuid.UUID, reason string) error {
	f.disarmedCalls++
	f.disarmedAcct = accountID
	f.disarmedNode = nodeID
	f.disarmReason = reason
	return nil
}

func (f *watchdogFakeNodeService) RecordPlatformRenderedFile(_ context.Context, _, _ uuid.UUID, path, content string) error {
	f.renderedCalls++
	f.renderedPath = path
	f.renderedText = content
	return nil
}

func watchdogTestServer(svc services.NodeService) *Server {
	return &Server{nodeService: svc, logger: slog.Default()}
}

func watchdogCmd() *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      node_command.SlugApplyWatchdogConfig,
		Payload:   map[string]interface{}{"config_hash": "payload-hash"},
	}
}

func TestSettleWatchdogApply(t *testing.T) {
	ctx := context.Background()

	t.Run("success records the hash and the rendered conf", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		conf := "watchdog-device = /dev/watchdog\n"
		watchdogTestServer(svc).settleWatchdogApply(ctx, watchdogCmd(), "success", "", map[string]string{
			"applied_hash": "device-hash",
			"conf":         conf,
		})
		if svc.appliedCalls != 1 || svc.appliedHash != "device-hash" {
			t.Errorf("applied: calls=%d hash=%q, want the device-reported hash once", svc.appliedCalls, svc.appliedHash)
		}
		if svc.renderedCalls != 1 || svc.renderedPath != node_file.WatchdogConfPath || svc.renderedText != conf {
			t.Errorf("rendered conf not recorded: calls=%d path=%q", svc.renderedCalls, svc.renderedPath)
		}
		if svc.refusedCalls != 0 {
			t.Errorf("a success must not record a refusal")
		}
	})

	t.Run("success without conf records no file version (disable path)", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		watchdogTestServer(svc).settleWatchdogApply(ctx, watchdogCmd(), "success", "", map[string]string{})
		if svc.appliedCalls != 1 || svc.appliedHash != "payload-hash" {
			t.Errorf("missing applied_hash must fall back to the payload's config_hash, got %q", svc.appliedHash)
		}
		if svc.renderedCalls != 0 {
			t.Errorf("no conf in the result must record no file version")
		}
	})

	t.Run("failed with a refusal records it with the details", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		watchdogTestServer(svc).settleWatchdogApply(ctx, watchdogCmd(), "failed", "10.0.0.1 did not answer", map[string]string{
			"refusal": "ping_target_unreachable",
		})
		if svc.refusedCalls != 1 || svc.refusal != "ping_target_unreachable" || svc.refusalDet != "10.0.0.1 did not answer" {
			t.Errorf("refusal not recorded: calls=%d code=%q details=%q", svc.refusedCalls, svc.refusal, svc.refusalDet)
		}
		if svc.appliedCalls != 0 || svc.renderedCalls != 0 {
			t.Errorf("a failure must record neither an applied hash nor a file version")
		}
	})

	t.Run("failed without a refusal records nothing", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		watchdogTestServer(svc).settleWatchdogApply(ctx, watchdogCmd(), "failed", "agent blew up", map[string]string{})
		if svc.refusedCalls != 0 || svc.appliedCalls != 0 || svc.renderedCalls != 0 {
			t.Errorf("a plain failure already surfaces via the command; the settle must add nothing")
		}
	})

	t.Run("non-terminal, foreign-slug and nil commands are ignored", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		srv := watchdogTestServer(svc)
		srv.settleWatchdogApply(ctx, watchdogCmd(), "in_progress", "", map[string]string{"conf": "x"})
		other := watchdogCmd()
		other.Slug = node_command.SlugReboot
		srv.settleWatchdogApply(ctx, other, "success", "", map[string]string{"conf": "x"})
		srv.settleWatchdogApply(ctx, nil, "success", "", map[string]string{"conf": "x"})
		if svc.appliedCalls != 0 || svc.refusedCalls != 0 || svc.renderedCalls != 0 {
			t.Errorf("nothing may settle: applied=%d refused=%d rendered=%d", svc.appliedCalls, svc.refusedCalls, svc.renderedCalls)
		}
	})
}

// The breaker's report must land in the node's watchdog facts — routed by
// recordWatchdogDisarm, which the StatusUpdate handler calls BEFORE the node
// connection-status whitelist, so "watchdog_disarmed" can never be written as
// a node status.
func TestRecordWatchdogDisarmRouting(t *testing.T) {
	ctx := context.Background()

	t.Run("valid stream identity records the disarm", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		nodeID, accountID := uuid.New(), uuid.New()
		watchdogTestServer(svc).recordWatchdogDisarm(ctx, nodeID.String(), accountID.String(), "restart_loop")
		if svc.disarmedCalls != 1 {
			t.Fatalf("RecordWatchdogDisarmed called %d times, want 1", svc.disarmedCalls)
		}
		if svc.disarmedNode != nodeID || svc.disarmedAcct != accountID || svc.disarmReason != "restart_loop" {
			t.Errorf("disarm routed with node=%s acct=%s reason=%q", svc.disarmedNode, svc.disarmedAcct, svc.disarmReason)
		}
	})

	t.Run("an unauthenticated frame is dropped", func(t *testing.T) {
		svc := &watchdogFakeNodeService{}
		watchdogTestServer(svc).recordWatchdogDisarm(ctx, "", "", "restart_loop")
		if svc.disarmedCalls != 0 {
			t.Errorf("a frame without a parseable identity must not be recorded")
		}
	})
}

// WatchdogDisarmedStatus must never be a valid node connection status; if it
// ever joins that set, the pre-whitelist routing in the StatusUpdate handler
// stops being the only writer and nodes.status can end up "watchdog_disarmed".
func TestWatchdogDisarmedIsNotANodeStatus(t *testing.T) {
	for _, s := range []node.NodeStatus{node.StatusOnline, node.StatusOffline, node.StatusReadyForAdoption} {
		if string(s) == node.WatchdogDisarmedStatus {
			t.Fatalf("%q collides with a node connection status", node.WatchdogDisarmedStatus)
		}
	}
}
