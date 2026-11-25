package grpc

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// The pacer bounds how often one device's stream does work; these pin the
// bound on how MUCH each frame may make the platform store and broadcast.

type sysInfoCountingNodeService struct {
	stubNodeService
	sysInfoUpdates int
}

func (s *sysInfoCountingNodeService) UpdateSystemInfo(context.Context, uuid.UUID, uuid.UUID, *node.SystemInfo) error {
	s.sysInfoUpdates++
	return nil
}

func systemInfoFrame(nodeID, accountID uuid.UUID, devices int) *proto.NodeToPlatform {
	storage := make(map[string]*proto.StorageDevice, devices)
	for i := 0; i < devices; i++ {
		name := "dev-" + strconv.Itoa(i)
		storage[name] = &proto.StorageDevice{Path: "/dev/" + name, Model: strings.Repeat("m", 64)}
	}
	return &proto.NodeToPlatform{
		NodeId:    nodeID.String(),
		AccountId: accountID.String(),
		Payload: &proto.NodeToPlatform_SystemInfoUpdate{
			SystemInfoUpdate: &proto.SystemInfoUpdate{StorageDevices: storage},
		},
	}
}

func TestOversizedSystemInfoReportIsNotStored(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &sysInfoCountingNodeService{stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}}
	srv := testServer(svc)

	// Thousands of storage entries: far past anything a device has, well
	// inside the frame ceiling.
	big := systemInfoFrame(nodeID, accountID, 5000)
	if size, tooLarge := systemInfoReportTooLarge(big.GetSystemInfoUpdate()); !tooLarge {
		t.Fatalf("fixture: %d bytes is not over the %d-byte bound", size, maxSystemInfoReportBytes)
	}

	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID), big},
	}
	if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("an oversized report must be dropped, not end the stream: %v", err)
	}
	if svc.sysInfoUpdates != 0 {
		t.Errorf("an oversized system-info report was stored (%d write(s)); it is written twice to the shared database and broadcast to every browser of the account", svc.sysInfoUpdates)
	}
}

func TestOrdinarySystemInfoReportIsStored(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &sysInfoCountingNodeService{stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}}
	srv := testServer(svc)

	stream := &scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID), systemInfoFrame(nodeID, accountID, 4)},
	}
	if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("establishing the stream: %v", err)
	}
	if svc.sysInfoUpdates != 1 {
		t.Errorf("an ordinary report was written %d time(s), want 1", svc.sysInfoUpdates)
	}
}

// resultCapturingCommandService records the result each status report would
// store.
type resultCapturingCommandService struct {
	stubNodeCommandService
	results []map[string]string
}

func (s *resultCapturingCommandService) UpdateNodeCommandStatusWithResult(_ context.Context, _ uuid.UUID, _ string, _ int, _ string, result map[string]string) (bool, error) {
	s.results = append(s.results, result)
	return true, nil
}

func TestOnlyATerminalReportStoresACommandResult(t *testing.T) {
	nodeID, accountID, commandID := uuid.New(), uuid.New(), uuid.New()
	svc := &commandStubNodeService{stubNodeService: stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}}
	srv := testServer(svc)
	cmds := &resultCapturingCommandService{stubNodeCommandService: stubNodeCommandService{
		owned: &node_command.NodeCommand{ID: commandID, NodeID: nodeID, Slug: "run_script"},
	}}
	srv.nodeCommandService = cmds

	withResult := func(st string) *proto.NodeToPlatform {
		m := commandStatus(nodeID, accountID, commandID, st)
		m.GetCommandStatusUpdate().Result = map[string]string{"stdout": strings.Repeat("x", 1024)}
		return m
	}
	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			withResult("in_progress"),
			withResult("success"),
		},
	}
	if err := srv.EstablishStream(stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("establishing the stream: %v", err)
	}
	if len(cmds.results) != 2 {
		t.Fatalf("got %d status update(s), want 2", len(cmds.results))
	}
	if cmds.results[0] != nil {
		t.Errorf("an in_progress report stored a result of %d key(s); a repeating non-terminal report must not rewrite the stored result", len(cmds.results[0]))
	}
	if len(cmds.results[1]) == 0 {
		t.Error("the terminal report's result was dropped; it is the block's declared output")
	}
}
