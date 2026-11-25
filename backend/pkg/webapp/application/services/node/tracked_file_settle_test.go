package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// A track or refresh read carries who asked, so the result settle can record
// the file; the mark comes from the tracked-file flow alone.
func TestTrackedFileReadIsMarkedAtDispatch(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)
	s.trackedFileRepo = &platformFakeTrackedRepo{}
	userID := uuid.New()

	_, err := s.ReadNodeFile(context.Background(), n.AccountID, userID, n.ID, "/etc/hosts", 4096)
	if err == nil {
		t.Fatal("expected the capture sentinel to surface as the dispatch failure")
	}
	if got, _ := capture.payload[results.TrackFileForKey].(string); got != userID.String() {
		t.Fatalf("the track read is not marked with its user: payload %+v", capture.payload)
	}
}

// A read_file from the generic command route or a playbook must not become a
// tracked file, whatever its params say.
func TestGenericReadFileCannotMarkItselfTracked(t *testing.T) {
	capture := &clampCapturingCommandService{}
	s, n := clampService(capture)

	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugReadFile,
		map[string]interface{}{"path": "/etc/hosts", results.TrackFileForKey: uuid.New().String()},
		payloadBoundAuth{accountID: n.AccountID}, nil, nil, "")
	if !errors.Is(err, errStopAfterCapture) {
		t.Fatalf("expected the capture sentinel, got %v", err)
	}
	if _, present := capture.payload[results.TrackFileForKey]; present {
		t.Fatalf("a caller's param marked a generic read as tracked: %+v", capture.payload)
	}
}

func trackedReadCommand(accountID, nodeID, userID uuid.UUID, path string) *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID: uuid.New(), AccountID: accountID, NodeID: nodeID,
		Slug:      node_command.SlugReadFile,
		Status:    node_command.StatusSuccess,
		CreatedAt: time.Now().Add(-time.Second),
		Payload: map[string]interface{}{
			"path": path, "max_bytes": float64(4096),
			results.TrackFileForKey: userID.String(),
		},
	}
}

// The device's result arriving is what records the file: no client has to be
// polling. A browser closed between the track and the result used to leave a
// read that succeeded and a file the platform never recorded.
func TestTrackedFileIsRecordedWhenTheReadSettles(t *testing.T) {
	files := &platformFakeFileService{}
	tracked := &platformFakeTrackedRepo{}
	svc := &Service{fileService: files, trackedFileRepo: tracked, logger: slog.Default()}
	accountID, nodeID, userID := uuid.New(), uuid.New(), uuid.New()

	cmd := trackedReadCommand(accountID, nodeID, userID, "/etc/hosts")
	result := map[string]string{"content": "127.0.0.1 localhost\n", "mode": "0644", "owner": "root", "group": "root"}
	if err := svc.CompleteTrackedFileRead(context.Background(), cmd, result); err != nil {
		t.Fatalf("CompleteTrackedFileRead: %v", err)
	}
	if tracked.tracked == nil || tracked.tracked.Path != "/etc/hosts" {
		t.Fatalf("the file was not tracked: %+v", tracked.tracked)
	}
	if tracked.tracked.CreatedBy != userID || tracked.tracked.Source != node_file.SourceUser {
		t.Errorf("tracked as %s by %s, want user by %s", tracked.tracked.Source, tracked.tracked.CreatedBy, userID)
	}
	if tracked.tracked.Mode != "0644" || tracked.tracked.Owner != "root" || tracked.tracked.LastReadAt == nil {
		t.Errorf("the read's metadata was not recorded: %+v", tracked.tracked)
	}
	if files.created != 1 || files.lastContent != result["content"] {
		t.Errorf("want one version with the read content, got %d (%q)", files.created, files.lastContent)
	}
}

// Only a read the tracked-file flow dispatched is recorded.
func TestUnmarkedReadIsNotTracked(t *testing.T) {
	files := &platformFakeFileService{}
	tracked := &platformFakeTrackedRepo{}
	svc := &Service{fileService: files, trackedFileRepo: tracked, logger: slog.Default()}

	cmd := trackedReadCommand(uuid.New(), uuid.New(), uuid.New(), "/etc/hosts")
	delete(cmd.Payload, results.TrackFileForKey)
	if err := svc.CompleteTrackedFileRead(context.Background(), cmd, map[string]string{"content": "x"}); err != nil {
		t.Fatalf("CompleteTrackedFileRead: %v", err)
	}
	if tracked.tracked != nil || files.created != 0 {
		t.Fatal("a read the tracked-file flow did not dispatch became a tracked file")
	}
}

// settledFileService is a version store that already holds the content the
// settle stored, created after the command: what a poll finds when it
// arrives late.
type settledFileService struct {
	platformFakeFileService
	stored *node_file.NodeFile
}

func (f *settledFileService) GetLatest(_ context.Context, _ uuid.UUID, _ string) (*node_file.NodeFile, error) {
	return f.stored, nil
}

func (f *settledFileService) CreateVersion(_ context.Context, _ uuid.UUID, _, _ string) (*node_file.NodeFile, error) {
	return f.stored, nil
}

type fixedCmdService struct {
	clampCapturingCommandService
	cmd *node_command.NodeCommand
}

func (f *fixedCmdService) GetNodeCommandByID(_ context.Context, _, _ uuid.UUID) (*node_command.NodeCommand, error) {
	return f.cmd, nil
}

// The read endpoint keeps working for clients that poll, and still reports
// that this read added a version when the settle stored it first.
func TestPollAfterTheSettleStillReportsTheNewVersion(t *testing.T) {
	accountID, nodeID, userID := uuid.New(), uuid.New(), uuid.New()
	cmd := trackedReadCommand(accountID, nodeID, userID, "/etc/hosts")
	cmd.Result = map[string]string{"content": "a\n"}
	stored := &node_file.NodeFile{ID: uuid.New(), NodeID: nodeID, Path: "/etc/hosts", Content: "a\n",
		Version: 3, Status: node_file.StatusApplied, CreatedAt: time.Now()}
	tracked := &platformFakeTrackedRepo{tracked: node_file.NewTrackedFile(accountID, nodeID, "/etc/hosts", node_file.SourceUser, userID)}
	svc := &Service{
		fileService:        &settledFileService{stored: stored},
		trackedFileRepo:    tracked,
		nodeCommandService: &fixedCmdService{cmd: cmd},
		logger:             slog.Default(),
	}

	out, err := svc.GetNodeFileReadResult(context.Background(), accountID, userID, nodeID, cmd.ID, "/etc/hosts", 4096, 50)
	if err != nil {
		t.Fatalf("GetNodeFileReadResult: %v", err)
	}
	if out.Status != "completed" || out.Version != 3 || !out.Changed {
		t.Fatalf("want completed, version 3, changed; got %+v", out)
	}

	// A version older than the command was there before this read: unchanged.
	stored.CreatedAt = cmd.CreatedAt.Add(-time.Hour)
	out, err = svc.GetNodeFileReadResult(context.Background(), accountID, userID, nodeID, cmd.ID, "/etc/hosts", 4096, 50)
	if err != nil {
		t.Fatalf("GetNodeFileReadResult: %v", err)
	}
	if out.Changed {
		t.Fatalf("a read that found the file as it was reported a change: %+v", out)
	}
}
