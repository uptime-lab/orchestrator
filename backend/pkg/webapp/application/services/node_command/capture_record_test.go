package node_command

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

type recordingCaptureSettler struct {
	failed []uuid.UUID
}

func (r *recordingCaptureSettler) FailAwaitingCapture(_ context.Context, _, imageID uuid.UUID) error {
	r.failed = append(r.failed, imageID)
	return nil
}

func captureCommand(status node_command.NodeCommandStatus) (node_command.NodeCommand, uuid.UUID) {
	c := cmd(node_command.SlugCaptureImage, status)
	imageID := uuid.New()
	c.Payload = map[string]interface{}{"image_id": imageID.String()}
	return c, imageID
}

// A capture_image that ends without streaming left its image record reading
// "capturing" until the next backend restart. Every route that settles the
// command as anything but success settles the record with it: the agent's own
// refusal on a live OS, a send that never left the platform, a stopped run.
func TestACaptureThatEndsWithoutStreamingSettlesItsRecord(t *testing.T) {
	paths := []struct {
		name   string
		status node_command.NodeCommandStatus
		settle func(svc *Service, c node_command.NodeCommand) error
	}{
		{"the agent refuses it", node_command.StatusInProgress,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.UpdateNodeCommandStatus(context.Background(), c.ID, "failed", 0, "must run from the netboot recovery bootstrap")
			}},
		{"the send fails before it leaves", node_command.StatusRegistered,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.MarkNodeCommandFailed(context.Background(), c.ID, "Node is not connected")
			}},
		{"the run is stopped", node_command.StatusInProgress,
			func(svc *Service, c node_command.NodeCommand) error {
				return svc.FailCommandOfStoppedRun(context.Background(), c.ID, "the run was cancelled")
			}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			c, imageID := captureCommand(p.status)
			svc, _, _, _ := stoppedRunService(c)
			settler := &recordingCaptureSettler{}
			svc.SetCaptureSettler(settler)

			if err := p.settle(svc, c); err != nil {
				t.Fatalf("settling: %v", err)
			}
			if len(settler.failed) != 1 || settler.failed[0] != imageID {
				t.Errorf("settled records %v, want exactly %s", settler.failed, imageID)
			}
		})
	}
}

// A capture that succeeded owns a finished record; and one still running is
// not settled by a progress report.
func TestACaptureThatSucceedsOrIsRunningLeavesItsRecordAlone(t *testing.T) {
	for _, report := range []string{"success", "in_progress"} {
		t.Run(report, func(t *testing.T) {
			c, _ := captureCommand(node_command.StatusInProgress)
			svc, _, _, _ := stoppedRunService(c)
			settler := &recordingCaptureSettler{}
			svc.SetCaptureSettler(settler)

			if err := svc.UpdateNodeCommandStatus(context.Background(), c.ID, report, 50, ""); err != nil {
				t.Fatalf("report: %v", err)
			}
			if len(settler.failed) != 0 {
				t.Errorf("a %s report settled the record", report)
			}
		})
	}
}
