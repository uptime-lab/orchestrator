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

// recordingNodeRepo captures the column writes so a test can ask whether the
// desired state was stored at all.
type recordingNodeRepo struct {
	repository.NodeRepository
	node    *node.Node
	written []map[string]interface{}
}

func (r *recordingNodeRepo) GetByID(_ context.Context, _, nodeID uuid.UUID) (*node.Node, error) {
	if r.node != nil {
		copied := *r.node
		copied.ID = nodeID
		return &copied, nil
	}
	return &node.Node{ID: nodeID}, nil
}

func (r *recordingNodeRepo) UpdateNodeColumns(_ context.Context, _, _ uuid.UUID, cols map[string]interface{}) error {
	r.written = append(r.written, cols)
	return nil
}

func (r *recordingNodeRepo) wroteColumn(name string) bool {
	for _, cols := range r.written {
		if _, ok := cols[name]; ok {
			return true
		}
	}
	return false
}

// A REFUSED apply must not change the stored desired state.
//
// The write used to happen before the dispatch, so an agent-too-old refusal —
// the common one — answered the caller an error while the stored config had
// already changed. The panel then showed a configuration the fleet can never
// converge to, and nothing would dispatch it again: the desired state already
// matched what the operator asked for.
//
// The dispatch is made to fail by leaving the command service unwired, which is
// what the fixture in led_gate_test.go already relies on.
func TestARefusedLEDApplyStoresNothing(t *testing.T) {
	repo := &recordingNodeRepo{}
	svc := &Service{
		nodeRepo:       repo,
		deviceProfiles: &device_profile.Catalog{Boards: []device_profile.BoardProfile{{ID: "bare-pi", Name: "Bare Pi"}}},
		deviceProfileResolver: &ledGateResolver{
			eq: device_profile.Equipage{ProfileID: "bare-pi"}, assigned: true,
		},
		logger: slog.Default(),
	}

	_, err := svc.SetLED(context.Background(), uuid.New(), uuid.New(), uuid.New(), node.LEDConfig{ActLEDOff: true})
	if err == nil {
		t.Fatal("the apply succeeded with no command service wired; this test can no longer observe a refusal")
	}
	if repo.wroteColumn("led_config") {
		t.Fatal("a refused LED apply stored the desired config anyway; the panel now shows a configuration the fleet can never converge to")
	}
}

// The same for the watchdog, where the consequence is worse: a device the
// operator believes is supervised and is not.
func TestARefusedWatchdogApplyStoresNothing(t *testing.T) {
	repo := &recordingNodeRepo{}
	svc := &Service{
		nodeRepo: repo,
		logger:   slog.Default(),
	}

	_, err := svc.SetWatchdog(context.Background(), uuid.New(), uuid.New(), uuid.New(), node.WatchdogConfig{
		Enabled:            true,
		OSHang:             true,
		WatchdogTimeoutSec: 30,
		IntervalSec:        10,
		RetryTimeoutSec:    60,
	})
	if err == nil {
		t.Fatal("the apply succeeded with no command service wired; this test can no longer observe a refusal")
	}
	if repo.wroteColumn("watchdog_config") {
		t.Fatal("a refused watchdog apply stored the desired config anyway; the panel now claims a supervision the device does not have")
	}
}

var _ = errors.Is
var _ = organizational_unit.SourceDefault
