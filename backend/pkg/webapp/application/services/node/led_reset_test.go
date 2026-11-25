package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// inheritingLEDResolver answers the LED config a unit declares.
type inheritingLEDResolver struct {
	ledGateResolver
	inherited node.LEDConfig
}

func (f *inheritingLEDResolver) ResolveEffectiveLEDConfig(context.Context, uuid.UUID, uuid.UUID) (node.LEDConfig, organizational_unit.PolicySource, bool, error) {
	return f.inherited, organizational_unit.SourceOU, true, nil
}

// Reset removes the node's own configuration: the column goes back to NULL,
// which is what lets the unit's policy resolve again. Storing an empty
// configuration instead pinned an all-off override that the node then kept
// for good, with no control to undo it.
func TestResetLEDClearsTheNodeOverride(t *testing.T) {
	repo := &recordingNodeRepo{node: &node.Node{LEDConfig: &node.LEDConfig{PwrLEDOff: true}}}
	svc := &Service{
		nodeRepo:       repo,
		deviceProfiles: &device_profile.Catalog{Boards: []device_profile.BoardProfile{{ID: "bare-pi", Name: "Bare Pi"}}},
		deviceProfileResolver: &inheritingLEDResolver{
			ledGateResolver: ledGateResolver{eq: device_profile.Equipage{ProfileID: "bare-pi"}, assigned: true},
			inherited:       node.LEDConfig{StealthMode: true},
		},
		logger: slog.Default(),
	}

	_, err := svc.ResetLED(context.Background(), uuid.New(), uuid.New(), uuid.New())

	var cleared bool
	for _, cols := range repo.written {
		if v, ok := cols["led_config"]; ok {
			if v != nil {
				t.Fatalf("reset stored led_config = %#v, want NULL", v)
			}
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("reset did not clear the node's own LED configuration")
	}
	// The converge is built from the INHERITED config: the unit asks for
	// stealth, which this board cannot do, so the gate refuses it.
	if !errors.Is(err, node.ErrLEDNotSupported) {
		t.Fatalf("reset err = %v, want the board gate's refusal of the inherited config", err)
	}
}
