package node

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// buttonEventRepo answers the one lookup the press fan-out performs; any other
// repo call panics through the embedded nil interface.
type buttonEventRepo struct {
	repository.NodeRepository
	n *node.Node
}

func (r *buttonEventRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return r.n, nil
}

// buttonEventSocket keeps the last message instead of delivering it.
type buttonEventSocket struct {
	sent []websockets3.Message
}

func (s *buttonEventSocket) Send(m websockets3.Message) error {
	s.sent = append(s.sent, m)
	return nil
}

func (s *buttonEventSocket) GetChannel() chan websockets3.Message { return nil }

func pressPayload(t *testing.T, sock *buttonEventSocket) map[string]string {
	t.Helper()
	if len(sock.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sock.sent))
	}
	wrapper, ok := sock.sent[0].Payload.(websockets3.MessageWrapper)
	if !ok {
		t.Fatalf("payload is %T, want MessageWrapper", sock.sent[0].Payload)
	}
	if wrapper.Type != "node_button_pressed" {
		t.Fatalf("event type %q, want node_button_pressed", wrapper.Type)
	}
	fields, ok := wrapper.Payload.(map[string]string)
	if !ok {
		t.Fatalf("wrapper payload is %T, want map[string]string", wrapper.Payload)
	}
	return fields
}

// The press event carries the duration as ITSELF, not only as an end time: a
// browser that subtracts blink_until from its own clock loses the flight time
// (a 300-second setting reads as 299) plus any clock skew, and then tells the
// operator "blinking for 299 seconds" about the same setting the identify
// button calls five minutes.
func TestButtonPressEventCarriesTheDuration(t *testing.T) {
	nodeID := uuid.New()
	sock := &buttonEventSocket{}
	s := &Service{
		nodeRepo:        &buttonEventRepo{n: &node.Node{ID: nodeID, Name: "Node-abcd0004"}},
		websocketClient: sock,
	}

	if err := s.NotifyButtonPressed(context.Background(), uuid.New(), nodeID, "blink_started"); err != nil {
		t.Fatalf("NotifyButtonPressed: %v", err)
	}
	fields := pressPayload(t, sock)
	if got := fields["duration_sec"]; got != "300" {
		t.Fatalf("duration_sec = %q, want the account's 300", got)
	}
	if fields["blink_until"] == "" {
		t.Error("blink_until must still ride the event — hydration needs the end time")
	}
}

// A press that did not start a blink has no duration to report, and an empty
// or zero field would be a number the browser could take at face value.
func TestStoppingPressCarriesNoDuration(t *testing.T) {
	nodeID := uuid.New()
	sock := &buttonEventSocket{}
	s := &Service{
		nodeRepo:        &buttonEventRepo{n: &node.Node{ID: nodeID, Name: "Node-abcd0004"}},
		websocketClient: sock,
	}

	if err := s.NotifyButtonPressed(context.Background(), uuid.New(), nodeID, "blink_stopped"); err != nil {
		t.Fatalf("NotifyButtonPressed: %v", err)
	}
	fields := pressPayload(t, sock)
	if _, ok := fields["duration_sec"]; ok {
		t.Error("a stopping press must not carry a blink duration")
	}
	if _, ok := fields["blink_until"]; ok {
		t.Error("a stopping press must not carry a blink end")
	}
}

// The duration in the event has to be the one THE DEVICE HAS. It blinks for the
// value baked in at its last device-profile apply; the account setting is what
// somebody may have changed since, and using it made the browser lamp fade
// early or stay lit over a dark node.
func TestButtonPressEventCarriesTheAppliedDuration(t *testing.T) {
	nodeID := uuid.New()
	sock := &buttonEventSocket{}
	s := &Service{
		nodeRepo:        &buttonEventRepo{n: &node.Node{ID: nodeID, Name: "Node-abcd0004"}},
		websocketClient: sock,
		logger:          slog.Default(),
		nodeCommandService: &durationFakeCommands{history: []node_command.NodeCommand{
			profileCmd(node_command.StatusSuccess, map[string]interface{}{"identify_duration_sec": float64(45)}),
		}},
		identifyDuration: func(context.Context, uuid.UUID) int { return 300 },
	}

	if err := s.NotifyButtonPressed(context.Background(), uuid.New(), nodeID, "blink_started"); err != nil {
		t.Fatalf("NotifyButtonPressed: %v", err)
	}
	if got := pressPayload(t, sock)["duration_sec"]; got != "45" {
		t.Fatalf("duration_sec = %q, want the 45 the device was given, not the setting's 300", got)
	}
}
