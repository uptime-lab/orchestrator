package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

type ledGateNodeRepo struct {
	repository.NodeRepository
}

func (f *ledGateNodeRepo) GetByID(_ context.Context, _, nodeID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: nodeID}, nil
}

type ledGateResolver struct {
	eq       device_profile.Equipage
	assigned bool
	err      error
}

func (f *ledGateResolver) ResolveEffectiveDeviceProfile(context.Context, uuid.UUID, uuid.UUID) (device_profile.Equipage, organizational_unit.PolicySource, bool, error) {
	return f.eq, organizational_unit.PolicySource(""), f.assigned, f.err
}

func (f *ledGateResolver) ResolveEffectiveLEDConfig(context.Context, uuid.UUID, uuid.UUID) (node.LEDConfig, organizational_unit.PolicySource, bool, error) {
	return node.LEDConfig{}, organizational_unit.SourceDefault, false, nil
}

func (f *ledGateResolver) ResolveEffectiveWatchdogConfig(context.Context, uuid.UUID, uuid.UUID) (node.WatchdogConfig, organizational_unit.PolicySource, bool, error) {
	return node.WatchdogConfig{}, organizational_unit.SourceDefault, false, nil
}

func ledGateFixture(resolver *ledGateResolver) *Service {
	catalog := &device_profile.Catalog{Boards: []device_profile.BoardProfile{
		{ID: "compute-blade", Name: "Compute Blade", Identify: device_profile.IdentifySpec{
			Backend: "neopixel", GPIO: 18, Color: "blue", StealthGPIO: 21, DigitalLEDs: 2,
		}},
		{ID: "bare-pi", Name: "Bare Pi"}, // sys-led fallback, no wiring
	}}
	return &Service{
		nodeRepo:              &ledGateNodeRepo{},
		deviceProfiles:        catalog,
		deviceProfileResolver: resolver,
		logger:                slog.Default(),
	}
}

// The board-gate matrix: what SetLED must refuse BEFORE any dispatch, decided
// by the effective device profile's wiring facts.
func TestSetLEDBoardGateMatrix(t *testing.T) {
	blade := &ledGateResolver{eq: device_profile.Equipage{ProfileID: "compute-blade"}, assigned: true}
	barePi := &ledGateResolver{eq: device_profile.Equipage{ProfileID: "bare-pi"}, assigned: true}
	unassigned := &ledGateResolver{}

	cases := []struct {
		name     string
		resolver *ledGateResolver
		cfg      node.LEDConfig
		wantErr  error // nil = must pass the gate (and then fail later at dispatch, which the fixture does not wire)
	}{
		{"stealth on a board with the circuit", blade, node.LEDConfig{StealthMode: true}, nil},
		{"stealth without a profile", unassigned, node.LEDConfig{StealthMode: true}, node.ErrLEDNotSupported},
		{"stealth on a board without the circuit", barePi, node.LEDConfig{StealthMode: true}, node.ErrLEDNotSupported},
		{"digital sources without a profile", unassigned, node.LEDConfig{DigitalSources: []string{"temperature"}}, node.ErrLEDNotSupported},
		{"digital sources on a board without digital LEDs", barePi, node.LEDConfig{DigitalSources: []string{"temperature"}}, node.ErrLEDNotSupported},
		{"more sources than the chain carries", blade, node.LEDConfig{DigitalSources: []string{"load", "load", "load"}}, node.ErrLEDNotSupported},
		{"sources within the chain", blade, node.LEDConfig{DigitalSources: []string{"temperature", "load"}}, nil},
		{"unknown source name", blade, node.LEDConfig{DigitalSources: []string{"disco"}}, node.ErrLEDInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				// A case expected to PASS the gate reaches the unwired dispatch
				// path and panics; that panic is the proof the gate let it by.
				if r := recover(); r != nil && tc.wantErr != nil {
					t.Fatalf("refusal case must not reach dispatch (panicked: %v)", r)
				}
			}()
			_, err := ledGateFixture(tc.resolver).SetLED(context.Background(), uuid.New(), uuid.New(), uuid.New(), tc.cfg)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// A resolver failure REFUSES the write instead of gating against empty
// capabilities: "this board cannot" for a read blip is a lie, and the dispatch
// params would carry GPIO 0 in place of the wiring.
func TestSetLEDRefusesOnResolverError(t *testing.T) {
	svc := ledGateFixture(&ledGateResolver{err: errors.New("db timeout")})
	_, err := svc.SetLED(context.Background(), uuid.New(), uuid.New(), uuid.New(), node.LEDConfig{StealthMode: true})
	if err == nil || errors.Is(err, node.ErrLEDNotSupported) {
		t.Fatalf("want a resolution failure, not a board refusal; got %v", err)
	}
}

// GetLED must answer with capabilities marked UNKNOWN on a resolver failure —
// empty capabilities render as "this board has none", which is a product
// answer the platform never established.
func TestGetLEDMarksCapabilitiesUnknownOnResolverError(t *testing.T) {
	svc := ledGateFixture(&ledGateResolver{err: errors.New("db timeout")})
	state, err := svc.GetLED(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("read path must still answer: %v", err)
	}
	if !state.CapabilitiesUnknown {
		t.Fatal("want capabilities_unknown=true on a resolver failure")
	}
}
