package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// durationFakeCommands serves a fixed history; the embedded nil interface
// panics on anything else, so the lookup cannot quietly grow.
type durationFakeCommands struct {
	services.NodeCommandService
	history []node_command.NodeCommand
	err     error
	calls   int
}

func (f *durationFakeCommands) GetNodeCommandsByNodeID(context.Context, uuid.UUID, uuid.UUID, int, int) ([]node_command.NodeCommand, error) {
	f.calls++
	return f.history, f.err
}

func durationService(history []node_command.NodeCommand, err error, accountSetting int) *Service {
	return &Service{
		logger:             slog.Default(),
		nodeCommandService: &durationFakeCommands{history: history, err: err},
		identifyDuration: func(context.Context, uuid.UUID) int {
			return accountSetting
		},
	}
}

func profileCmd(status node_command.NodeCommandStatus, payload map[string]interface{}) node_command.NodeCommand {
	return node_command.NodeCommand{
		ID: uuid.New(), Slug: node_command.SlugApplyDeviceProfile, Status: status, Payload: payload,
	}
}

// The device blinks for the duration it was GIVEN at its last apply. The press
// event used the current account setting, so after any change the browser lamp
// and the physical LED disagreed — an early fade, or a lamp still lit over a
// dark node.
func TestButtonDurationFollowsTheDeviceNotTheSetting(t *testing.T) {
	svc := durationService([]node_command.NodeCommand{
		profileCmd(node_command.StatusSuccess, map[string]interface{}{"identify_duration_sec": float64(60)}),
	}, nil, 300)

	if got := svc.appliedIdentifyDuration(context.Background(), uuid.New(), uuid.New()); got != 60 {
		t.Fatalf("duration = %d, want the 60 the device was given, not the 300 the setting says now", got)
	}
}

// Only the NEWEST successful apply counts: an older one describes a device
// state that has been replaced.
func TestButtonDurationTakesTheNewestSuccessfulApply(t *testing.T) {
	svc := durationService([]node_command.NodeCommand{
		profileCmd(node_command.StatusFailed, map[string]interface{}{"identify_duration_sec": float64(15)}),
		profileCmd(node_command.StatusSuccess, map[string]interface{}{"identify_duration_sec": float64(60)}),
		profileCmd(node_command.StatusSuccess, map[string]interface{}{"identify_duration_sec": float64(120)}),
	}, nil, 300)

	if got := svc.appliedIdentifyDuration(context.Background(), uuid.New(), uuid.New()); got != 60 {
		t.Fatalf("duration = %d, want 60 from the newest successful apply", got)
	}
}

// Three ways to have no answer, all of which mean the account setting IS what
// the device will use — because the next apply is what will put it there.
func TestButtonDurationFallsBackToTheSetting(t *testing.T) {
	cases := map[string]*Service{
		"no apply in history":        durationService(nil, nil, 300),
		"the payload predates it":    durationService([]node_command.NodeCommand{profileCmd(node_command.StatusSuccess, map[string]interface{}{})}, nil, 300),
		"zero is not a duration":     durationService([]node_command.NodeCommand{profileCmd(node_command.StatusSuccess, map[string]interface{}{"identify_duration_sec": float64(0)})}, nil, 300),
		"the history cannot be read": durationService(nil, errors.New("db down"), 300),
	}
	for name, svc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := svc.appliedIdentifyDuration(context.Background(), uuid.New(), uuid.New()); got != 300 {
				t.Fatalf("duration = %d, want the account setting", got)
			}
		})
	}
}

// Without the command service wired the lookup must not be attempted at all —
// the press still has to fan out.
func TestButtonDurationWithoutCommandHistory(t *testing.T) {
	svc := &Service{logger: slog.Default(), identifyDuration: func(context.Context, uuid.UUID) int { return 300 }}
	if got := svc.appliedIdentifyDuration(context.Background(), uuid.New(), uuid.New()); got != 300 {
		t.Fatalf("duration = %d, want the account setting", got)
	}
}
