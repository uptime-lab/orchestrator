package grpc

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// The embedded nil interfaces panic on anything else, proving the settle
// touches only these two.
type reimageFakeNodeService struct {
	services.NodeService
	calls   int
	stamped int
}

func (f *reimageFakeNodeService) InvalidateAppliedStateAfterReimage(context.Context, uuid.UUID, uuid.UUID) error {
	f.calls++
	return nil
}

// The settle now also stamps "the platform expects this node back" — a
// successful install reboots the device into the new OS from there.
func (f *reimageFakeNodeService) MarkAwaitingReturn(context.Context, uuid.UUID, string) error {
	f.stamped++
	return nil
}

type reimageFakeSettler struct {
	calls int
}

func (f *reimageFakeSettler) SettleApply(context.Context, *node_command.NodeCommand, string, map[string]string) {
}

func (f *reimageFakeSettler) ForgetAppliedAfterReimage(context.Context, uuid.UUID, uuid.UUID) error {
	f.calls++
	return nil
}

func reimageCmd(slug string) *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID: uuid.New(), NodeID: uuid.New(), AccountID: uuid.New(), Slug: slug,
	}
}

func TestSettleReimageForgetsOnASuccessfulInstall(t *testing.T) {
	node := &reimageFakeNodeService{}
	utilities := &reimageFakeSettler{}
	srv := &Server{nodeService: node, utilitySettler: utilities, logger: slog.Default()}

	srv.settleReimage(context.Background(), reimageCmd(node_command.SlugInstallOS), "success")

	if node.calls != 1 || utilities.calls != 1 {
		t.Fatalf("want both cleared once, got node=%d utilities=%d", node.calls, utilities.calls)
	}
}

// Everything else must leave the markers alone. erase_drive is the one worth
// naming: it takes a target device and may be pointed at a data disk while the
// OS keeps running, so clearing on it would report a converged node as
// unconverged.
func TestSettleReimageIgnoresEveryOtherOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slug   string
		status string
	}{
		{"a failed install leaves the disk unbootable, not reimaged", node_command.SlugInstallOS, "failed"},
		{"progress is not an outcome", node_command.SlugInstallOS, "in_progress"},
		{"erase_drive may target a data disk", node_command.SlugEraseDrive, "success"},
		{"an unrelated command", node_command.SlugRunScript, "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &reimageFakeNodeService{}
			utilities := &reimageFakeSettler{}
			srv := &Server{nodeService: node, utilitySettler: utilities, logger: slog.Default()}

			srv.settleReimage(context.Background(), reimageCmd(tc.slug), tc.status)

			if node.calls != 0 || utilities.calls != 0 {
				t.Fatalf("cleared applied state anyway: node=%d utilities=%d", node.calls, utilities.calls)
			}
		})
	}
}
