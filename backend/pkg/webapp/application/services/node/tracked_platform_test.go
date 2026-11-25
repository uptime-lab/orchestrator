package node

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// platformFakeFileService covers the version-store calls the platform-rendered
// path makes; anything else panics through the embedded nil interface.
type platformFakeFileService struct {
	services.FileService
	created       int
	lastContent   string
	statusUpdates []string
}

func (f *platformFakeFileService) StoredBytes(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}

func (f *platformFakeFileService) GetLatest(_ context.Context, _ uuid.UUID, _ string) (*node_file.NodeFile, error) {
	return nil, node_file.ErrNotTracked
}

func (f *platformFakeFileService) CreateVersion(_ context.Context, _ uuid.UUID, path, content string) (*node_file.NodeFile, error) {
	f.created++
	f.lastContent = content
	return &node_file.NodeFile{Path: path, Content: content, Version: 1, Status: node_file.StatusSaved}, nil
}

func (f *platformFakeFileService) UpdateStatus(_ context.Context, _ uuid.UUID, _ string, _ int, status string, _ *string) error {
	f.statusUpdates = append(f.statusUpdates, status)
	return nil
}

// platformFakeTrackedRepo starts empty and records the create/update calls.
type platformFakeTrackedRepo struct {
	repository.NodeTrackedFileRepository
	tracked *node_file.TrackedFile
	updates int
}

func (f *platformFakeTrackedRepo) GetByNodeIDAndPath(_ context.Context, _ uuid.UUID, path string) (*node_file.TrackedFile, error) {
	if f.tracked == nil || f.tracked.Path != path {
		return nil, node_file.ErrNotTracked
	}
	return f.tracked, nil
}

func (f *platformFakeTrackedRepo) CountByNodeID(_ context.Context, _ uuid.UUID) (int64, error) {
	if f.tracked == nil {
		return 0, nil
	}
	return 1, nil
}

func (f *platformFakeTrackedRepo) Create(_ context.Context, t *node_file.TrackedFile) error {
	f.tracked = t
	return nil
}

func (f *platformFakeTrackedRepo) CreateUnderLimit(
	_ context.Context, t *node_file.TrackedFile, limit int,
) (*node_file.TrackedFile, error) {
	if f.tracked != nil && f.tracked.Path == t.Path {
		return f.tracked, nil
	}
	if limit > 0 && f.tracked != nil {
		return nil, node_file.ErrTrackedLimit
	}
	f.tracked = t
	return t, nil
}

func (f *platformFakeTrackedRepo) Update(_ context.Context, t *node_file.TrackedFile) error {
	f.updates++
	f.tracked = t
	return nil
}

// A successful watchdog apply reports the rendered /etc/watchdog.conf back and
// the settle records it as a platform tracked file. The path is protected in
// the path policy — this proves protected does not mean refused, and that the
// record lands as source=platform, applied, with no initiating user.
func TestRecordPlatformRenderedFileTracksWatchdogConf(t *testing.T) {
	files := &platformFakeFileService{}
	tracked := &platformFakeTrackedRepo{}
	svc := &Service{fileService: files, trackedFileRepo: tracked, logger: slog.Default()}

	accountID, nodeID := uuid.New(), uuid.New()
	conf := "watchdog-device = /dev/watchdog\ninterval = 10\n"
	if err := svc.RecordPlatformRenderedFile(context.Background(), accountID, nodeID, node_file.WatchdogConfPath, conf); err != nil {
		t.Fatalf("RecordPlatformRenderedFile: %v", err)
	}

	if files.created != 1 || files.lastContent != conf {
		t.Fatalf("expected one version with the rendered text, got %d (%q)", files.created, files.lastContent)
	}
	if len(files.statusUpdates) != 1 || files.statusUpdates[0] != node_file.StatusApplied {
		t.Errorf("the recorded version must be marked applied, got %v", files.statusUpdates)
	}
	if tracked.tracked == nil {
		t.Fatal("no tracked record created")
	}
	if tracked.tracked.Source != node_file.SourcePlatform {
		t.Errorf("source = %q, want platform", tracked.tracked.Source)
	}
	if tracked.tracked.CreatedBy != uuid.Nil {
		t.Errorf("created_by = %s, want uuid.Nil (node commands carry no user)", tracked.tracked.CreatedBy)
	}
	if tracked.tracked.Size != int64(len(conf)) || tracked.tracked.LastReadAt == nil {
		t.Errorf("tracked metadata not updated: size=%d last_read=%v", tracked.tracked.Size, tracked.tracked.LastReadAt)
	}

	// A second apply with the same tracked record must reuse it, not create.
	if err := svc.RecordPlatformRenderedFile(context.Background(), accountID, nodeID, node_file.WatchdogConfPath, conf+"# v2\n"); err != nil {
		t.Fatalf("second RecordPlatformRenderedFile: %v", err)
	}
	if files.created != 2 {
		t.Errorf("expected a second version, got %d", files.created)
	}
	if tracked.updates != 2 {
		t.Errorf("expected the tracked record to be updated twice, got %d", tracked.updates)
	}
}

// A refused path must be refused here too: the settle hands this method
// whatever path constant the server names, and the path policy is the only
// guard between that and a durable platform copy.
func TestRecordPlatformRenderedFileRefusesRefusedPaths(t *testing.T) {
	svc := &Service{fileService: &platformFakeFileService{}, trackedFileRepo: &platformFakeTrackedRepo{}, logger: slog.Default()}
	err := svc.RecordPlatformRenderedFile(context.Background(), uuid.New(), uuid.New(), "/etc/shadow", "x")
	if err == nil {
		t.Fatal("expected the refused path to be rejected")
	}
}
