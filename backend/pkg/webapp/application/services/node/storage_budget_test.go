package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// budgetFakeFileService stubs only what the budget check and the save path
// touch; anything else panics through the embedded nil interface.
type budgetFakeFileService struct {
	services.FileService
	stored    int64
	created   int
	lastBytes int
}

func (f *budgetFakeFileService) StoredBytes(_ context.Context, _ uuid.UUID) (int64, error) {
	return f.stored, nil
}

func (f *budgetFakeFileService) CreateVersion(_ context.Context, _ uuid.UUID, path, content string) (*node_file.NodeFile, error) {
	f.created++
	f.lastBytes = len(content)
	return &node_file.NodeFile{Path: path, Content: content, Version: 1}, nil
}

// budgetFakeTrackedRepo answers "is this path tracked" and nothing else.
type budgetFakeTrackedRepo struct {
	repository.NodeTrackedFileRepository
	tracked *node_file.TrackedFile
}

func (f *budgetFakeTrackedRepo) GetByNodeIDAndPath(_ context.Context, _ uuid.UUID, path string) (*node_file.TrackedFile, error) {
	if f.tracked == nil || f.tracked.Path != path {
		return nil, node_file.ErrNotTracked
	}
	return f.tracked, nil
}

func budgetService(files *budgetFakeFileService, tracked *budgetFakeTrackedRepo) *Service {
	return &Service{fileService: files, trackedFileRepo: tracked, logger: slog.Default()}
}

// Saving used to go straight to the version store, so a caller could create
// rows for any path at all and walk past the per-node file cap entirely.
func TestSaveRefusesAnUntrackedPath(t *testing.T) {
	files := &budgetFakeFileService{}
	svc := budgetService(files, &budgetFakeTrackedRepo{})

	_, err := svc.SaveNodeFileVersion(context.Background(), uuid.New(), uuid.New(), "/etc/hosts", "x")
	if !errors.Is(err, node_file.ErrNotTracked) {
		t.Fatalf("expected ErrNotTracked, got %v", err)
	}
	if files.created != 0 {
		t.Fatal("an untracked path must not create a version")
	}
}

func TestSaveRefusesAWriteBeyondTheNodeBudget(t *testing.T) {
	accountID := uuid.New()
	nodeID := uuid.New()
	files := &budgetFakeFileService{stored: node_file.MaxTotalBytesPerNode - 10}
	tracked := &budgetFakeTrackedRepo{tracked: &node_file.TrackedFile{
		Path: "/etc/hosts", AccountID: accountID, NodeID: nodeID,
	}}
	svc := budgetService(files, tracked)

	// The per-file and per-version caps multiply across paths; this is the
	// ceiling that keeps one node's worst case a number rather than a product.
	_, err := svc.SaveNodeFileVersion(context.Background(), accountID, nodeID, "/etc/hosts", "12345678901234567890")
	if !errors.Is(err, node_file.ErrTrackedLimit) {
		t.Fatalf("expected the budget to refuse the write, got %v", err)
	}
	if files.created != 0 {
		t.Fatal("a refused write must not create a version")
	}
}

func TestSaveAcceptsAWriteInsideTheBudget(t *testing.T) {
	accountID := uuid.New()
	nodeID := uuid.New()
	files := &budgetFakeFileService{stored: 1024}
	tracked := &budgetFakeTrackedRepo{tracked: &node_file.TrackedFile{
		Path: "/etc/hosts", AccountID: accountID, NodeID: nodeID,
	}}
	svc := budgetService(files, tracked)

	if _, err := svc.SaveNodeFileVersion(context.Background(), accountID, nodeID, "/etc/hosts", "127.0.0.1 localhost\n"); err != nil {
		t.Fatalf("expected an ordinary save to succeed, got %v", err)
	}
	if files.created != 1 {
		t.Fatalf("expected one version to be created, got %d", files.created)
	}
}

// A tracked row belonging to another account must not be writable even when the
// node id lines up.
func TestSaveRefusesAnotherAccountsTrackedFile(t *testing.T) {
	nodeID := uuid.New()
	files := &budgetFakeFileService{}
	tracked := &budgetFakeTrackedRepo{tracked: &node_file.TrackedFile{
		Path: "/etc/hosts", AccountID: uuid.New(), NodeID: nodeID,
	}}
	svc := budgetService(files, tracked)

	if _, err := svc.SaveNodeFileVersion(context.Background(), uuid.New(), nodeID, "/etc/hosts", "x"); err == nil {
		t.Fatal("expected a cross-account save to be refused")
	}
	if files.created != 0 {
		t.Fatal("a refused save must not create a version")
	}
}
