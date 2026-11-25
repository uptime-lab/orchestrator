package grpc

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// ledFakeNodeService records RecordLEDApplied; the embedded nil interface
// panics on anything else, proving the settle touches nothing more.
type ledFakeNodeService struct {
	services.NodeService

	recorded    string
	recordCalls int
}

func (f *ledFakeNodeService) RecordLEDApplied(_ context.Context, _, _ uuid.UUID, hash string) error {
	f.recordCalls++
	f.recorded = hash
	return nil
}

func ledCmd(payload map[string]interface{}) *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      node_command.SlugApplyLEDConfig,
		Payload:   payload,
	}
}

// The hash is recorded from the DISPATCH payload on success only.
func TestSettleLEDApplyRecordsDispatchHashOnSuccess(t *testing.T) {
	fake := &ledFakeNodeService{}
	srv := &Server{nodeService: fake, logger: slog.Default()}
	srv.settleLEDApply(context.Background(), ledCmd(map[string]interface{}{"config_hash": "h-1"}), "success")
	if fake.recordCalls != 1 || fake.recorded != "h-1" {
		t.Fatalf("want the payload hash recorded once, got %d calls %q", fake.recordCalls, fake.recorded)
	}
}

// A FAILED apply must record nothing: the desired hash it carries is the state
// the device was asked for, not one it reached — recording it would render the
// panel in_sync over a converge that never happened.
func TestSettleLEDApplyIgnoresNonSuccessAndEmptyHash(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		payload map[string]interface{}
	}{
		{"failed status", "failed", map[string]interface{}{"config_hash": "h-1"}},
		{"skipped status", "skipped", map[string]interface{}{"config_hash": "h-1"}},
		{"success with no hash in the payload", "success", map[string]interface{}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &ledFakeNodeService{}
			srv := &Server{nodeService: fake, logger: slog.Default()}
			srv.settleLEDApply(context.Background(), ledCmd(tc.payload), tc.status)
			if fake.recordCalls != 0 {
				t.Fatalf("want nothing recorded, got %d calls %q", fake.recordCalls, fake.recorded)
			}
		})
	}
}

// A foreign slug never records — the settle is keyed on apply_led_config.
func TestSettleLEDApplyIgnoresOtherSlugs(t *testing.T) {
	fake := &ledFakeNodeService{}
	srv := &Server{nodeService: fake, logger: slog.Default()}
	cmd := ledCmd(map[string]interface{}{"config_hash": "h-1"})
	cmd.Slug = node_command.SlugReboot
	srv.settleLEDApply(context.Background(), cmd, "success")
	if fake.recordCalls != 0 {
		t.Fatalf("want nothing recorded for a foreign slug, got %d calls", fake.recordCalls)
	}
}
