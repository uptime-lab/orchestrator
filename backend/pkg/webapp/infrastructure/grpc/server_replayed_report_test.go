package grpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// The four settlers in the CommandStatusUpdate branch settle a TRANSITION.
// Command ownership is checked against the account and the node, and terminal
// commands stay readable for the whole retention window, so a node can replay a
// terminal frame for one of its own long-finished commands. Every settler would
// then run again — and the watchdog one re-stamps the applied hash, which blanks
// the recorded disarm reason: the device erases the very signal that says its
// breaker disarmed it.

// replayFakeNodeService answers the command-status path and counts the watchdog
// settle. The embedded interface is nil, so anything else panics loudly.
type replayFakeNodeService struct {
	commandStubNodeService
	watchdogApplied int
}

func (f *replayFakeNodeService) RecordWatchdogApplied(context.Context, uuid.UUID, uuid.UUID, string) error {
	f.watchdogApplied++
	return nil
}

func watchdogApplyReport(nodeID, accountID, commandID uuid.UUID) *proto.NodeToPlatform {
	return commandStatus(nodeID, accountID, commandID, string(node_command.StatusSuccess))
}

func replayTestServer(nodeID, accountID, commandID uuid.UUID, applied bool) (*Server, *replayFakeNodeService) {
	svc := &replayFakeNodeService{
		commandStubNodeService: commandStubNodeService{
			stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}},
		},
	}
	srv := testServer(svc)
	srv.nodeCommandService = &stubNodeCommandService{
		owned: &node_command.NodeCommand{
			ID:      commandID,
			NodeID:  nodeID,
			Slug:    node_command.SlugApplyWatchdogConfig,
			Payload: map[string]interface{}{"config_hash": "a-hash"},
		},
		notApplied: !applied,
	}
	return srv, svc
}

// A report the status update refused to apply must settle nothing.
func TestReplayedTerminalReportDoesNotReDriveTheWatchdogSettle(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	srv, svc := replayTestServer(nodeID, accountID, commandID, false)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			watchdogApplyReport(nodeID, accountID, commandID),
		},
	}

	// The peer going away ends the loop with EOF; what matters is what ran.
	if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("the stream must survive an ignored report: %v", err)
	}
	if svc.watchdogApplied != 0 {
		t.Errorf("the watchdog settle ran %d time(s) for a command that was already finished; "+
			"re-stamping the applied hash blanks a recorded breaker disarm", svc.watchdogApplied)
	}
}

// The log says a status was updated only when it was: an ignored report for a
// settled command must not also read as "command status updated … success".
func TestOnlyAnAppliedReportIsLoggedAsAnUpdate(t *testing.T) {
	for _, applied := range []bool{false, true} {
		nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
		srv, _ := replayTestServer(nodeID, accountID, commandID, applied)
		var logs bytes.Buffer
		srv.logger = slog.New(slog.NewTextHandler(&logs, nil))

		stream := &scriptedStream{
			ctx: context.Background(),
			incoming: []*proto.NodeToPlatform{
				connectionInit(nodeID, accountID),
				watchdogApplyReport(nodeID, accountID, commandID),
			},
		}
		if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("establishing the stream: %v", err)
		}
		if logged := strings.Contains(logs.String(), `msg="command status updated"`); logged != applied {
			t.Errorf("applied=%v: \"command status updated\" logged=%v, want %v", applied, logged, applied)
		}
	}
}

// And the gate must not be a blanket off switch: a report that DOES move the
// command still settles.
func TestAppliedReportStillSettlesTheWatchdogApply(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	srv, svc := replayTestServer(nodeID, accountID, commandID, true)

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			watchdogApplyReport(nodeID, accountID, commandID),
		},
	}

	if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("establishing the stream: %v", err)
	}
	if svc.watchdogApplied != 1 {
		t.Errorf("the watchdog settle ran %d time(s) for a real terminal report, want exactly 1", svc.watchdogApplied)
	}
}
