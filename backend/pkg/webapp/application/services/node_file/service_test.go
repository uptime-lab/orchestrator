package node_file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// fakeRepo is an in-memory NodeFileRepository. Only the methods the service
// touches are meaningful; the rest satisfy the interface.
type fakeRepo struct {
	files []node_file.NodeFile
}

// CreateNextVersion allocates the version the way the real repository does —
// from the stored rows, not from the caller — so a test cannot pass by handing
// in a number the production path would never accept.
func (r *fakeRepo) CreateNextVersion(_ context.Context, f *node_file.NodeFile) error {
	next := 0
	for _, existing := range r.files {
		if existing.NodeID == f.NodeID && existing.Path == f.Path && existing.Version > next {
			next = existing.Version
		}
	}
	f.Version = next + 1
	r.files = append(r.files, *f)
	return nil
}

func (r *fakeRepo) Update(_ context.Context, f *node_file.NodeFile) error {
	for i := range r.files {
		if r.files[i].ID == f.ID {
			r.files[i] = *f
			return nil
		}
	}
	return errors.New("not found")
}

func (r *fakeRepo) GetByNodeIDAndPath(_ context.Context, nodeID uuid.UUID, path string) ([]node_file.NodeFile, error) {
	var out []node_file.NodeFile
	for _, f := range r.files {
		if f.NodeID == nodeID && f.Path == path {
			out = append(out, f)
		}
	}
	return out, nil
}

func (r *fakeRepo) GetLatestByNodeIDAndPath(_ context.Context, nodeID uuid.UUID, path string) (*node_file.NodeFile, error) {
	var latest *node_file.NodeFile
	for i := range r.files {
		f := r.files[i]
		if f.NodeID != nodeID || f.Path != path {
			continue
		}
		if latest == nil || f.Version > latest.Version {
			cp := f
			latest = &cp
		}
	}
	if latest == nil {
		return nil, errors.New("not found")
	}
	return latest, nil
}

func (r *fakeRepo) GetByNodeIDPathAndVersion(_ context.Context, nodeID uuid.UUID, path string, version int) (*node_file.NodeFile, error) {
	for i := range r.files {
		if r.files[i].NodeID == nodeID && r.files[i].Path == path && r.files[i].Version == version {
			cp := r.files[i]
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (r *fakeRepo) GetAllPathsByNodeID(_ context.Context, nodeID uuid.UUID) ([]node_file.NodeFile, error) {
	return r.files, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (*node_file.NodeFile, error) {
	for i := range r.files {
		if r.files[i].ID == id {
			cp := r.files[i]
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (r *fakeRepo) DeleteByNodeIDAndPath(_ context.Context, nodeID uuid.UUID, path string) error {
	kept := r.files[:0]
	for _, f := range r.files {
		if f.NodeID != nodeID || f.Path != path {
			kept = append(kept, f)
		}
	}
	r.files = kept
	return nil
}

func (r *fakeRepo) DeleteByNodeID(_ context.Context, nodeID uuid.UUID) error {
	kept := r.files[:0]
	for _, f := range r.files {
		if f.NodeID != nodeID {
			kept = append(kept, f)
		}
	}
	r.files = kept
	return nil
}

func (r *fakeRepo) SumContentBytesByNodeID(_ context.Context, nodeID uuid.UUID) (int64, error) {
	var total int64
	for _, f := range r.files {
		if f.NodeID == nodeID {
			total += int64(len(f.Content))
		}
	}
	return total, nil
}

func newTestService() (*Service, *fakeRepo) {
	repo := &fakeRepo{}
	return NewService(repo, slog.New(slog.NewTextHandler(io.Discard, nil))), repo
}

const (
	testPath    = "/boot/firmware/config.txt"
	testContent = "dtparam=audio=on\n"
)

// testHash is the REAL sha256 of testContent: agent reports have their hash
// verified against the content, so a made-up hash is refused.
var testHash = func() string {
	sum := sha256.Sum256([]byte(testContent))
	return hex.EncodeToString(sum[:])
}()

// A failed apply must not add a version. A de-dupe gated on status == applied
// would leave v1 "applying" + v2 "failed" for a single file after one failed
// apply.
func TestFailedReportDoesNotCreateAVersion(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	nodeID := uuid.New()

	if _, err := svc.CreateVersion(ctx, nodeID, testPath, testContent); err != nil {
		t.Fatalf("create version: %v", err)
	}
	stored, _ := repo.GetLatestByNodeIDAndPath(ctx, nodeID, testPath)

	msg := "write failed"
	if _, err := svc.CreateOrUpdateFromAgent(ctx, nodeID, testPath, testContent, stored.Hash, 0, node_file.StatusFailed, &msg); err != nil {
		t.Fatalf("agent report: %v", err)
	}

	versions, _ := repo.GetByNodeIDAndPath(ctx, nodeID, testPath)
	if len(versions) != 1 {
		t.Fatalf("expected the failed report to update the existing version, got %d versions", len(versions))
	}
	if versions[0].Status != node_file.StatusFailed {
		t.Fatalf("expected status failed, got %q", versions[0].Status)
	}
}

// The agent fills content only for the paths it watches; for anything else it
// reports empty strings. Storing that would blank a tracked file and a later
// Apply would write an empty file to the device.
func TestEmptyReportNeverBlanksStoredContent(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	nodeID := uuid.New()

	if _, err := svc.CreateVersion(ctx, nodeID, "/etc/hosts", "127.0.0.1 localhost\n"); err != nil {
		t.Fatalf("create version: %v", err)
	}

	if _, err := svc.CreateOrUpdateFromAgent(ctx, nodeID, "/etc/hosts", "", "", 0, node_file.StatusApplied, nil); err != nil {
		t.Fatalf("agent report: %v", err)
	}

	latest, err := repo.GetLatestByNodeIDAndPath(ctx, nodeID, "/etc/hosts")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Content == "" {
		t.Fatal("stored content was blanked by a contentless agent report")
	}
	if latest.Status != node_file.StatusApplied {
		t.Fatalf("the report should still move the status, got %q", latest.Status)
	}
}

// A report naming a version that does not exist used to create it, letting a
// stale agent invent history. It must be rejected instead.
func TestReportForUnknownVersionIsRejected(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	_, err := svc.CreateOrUpdateFromAgent(ctx, uuid.New(), testPath, testContent, testHash, 7, node_file.StatusApplied, nil)
	if !errors.Is(err, node_file.ErrNotTracked) {
		t.Fatalf("expected ErrNotTracked, got %v", err)
	}
}

// The same content reported twice must not grow the history.
func TestRepeatedIdenticalReportIsIdempotent(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	nodeID := uuid.New()

	for i := 0; i < 3; i++ {
		if _, err := svc.CreateOrUpdateFromAgent(ctx, nodeID, testPath, testContent, testHash, 0, node_file.StatusApplied, nil); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}

	versions, _ := repo.GetByNodeIDAndPath(ctx, nodeID, testPath)
	if len(versions) != 1 {
		t.Fatalf("expected 1 version for three identical reports, got %d", len(versions))
	}
}

// PruneVersions keeps the history bounded: each row holds the whole file, so an
// unbounded history is a table that grows with every save.
func (r *fakeRepo) PruneVersions(_ context.Context, nodeID uuid.UUID, path string, keep int) error {
	var versions []int
	for _, f := range r.files {
		if f.NodeID == nodeID && f.Path == path {
			versions = append(versions, f.Version)
		}
	}
	if len(versions) <= keep {
		return nil
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))
	cutoff := versions[keep-1]
	kept := r.files[:0]
	for _, f := range r.files {
		if f.NodeID == nodeID && f.Path == path && f.Version < cutoff {
			continue
		}
		kept = append(kept, f)
	}
	r.files = kept
	return nil
}

func TestVersionHistoryIsBounded(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	nodeID := uuid.New()

	for i := 0; i < node_file.MaxVersionsPerPath+5; i++ {
		if _, err := svc.CreateVersion(ctx, nodeID, "/etc/hosts", fmt.Sprintf("line %d\n", i)); err != nil {
			t.Fatalf("create version %d: %v", i, err)
		}
	}

	versions, _ := repo.GetByNodeIDAndPath(ctx, nodeID, "/etc/hosts")
	if len(versions) > node_file.MaxVersionsPerPath {
		t.Fatalf("expected at most %d versions, got %d", node_file.MaxVersionsPerPath, len(versions))
	}
	latest, _ := repo.GetLatestByNodeIDAndPath(ctx, nodeID, "/etc/hosts")
	if latest.Version != node_file.MaxVersionsPerPath+5 {
		t.Fatalf("pruning must drop the OLDEST versions, not the newest; latest is v%d", latest.Version)
	}
}

// ListLatestVersionMeta mirrors the SQL repository: newest version per path,
// metadata only.
func (r *fakeRepo) ListLatestVersionMeta(_ context.Context, nodeID uuid.UUID) ([]node_file.VersionMeta, error) {
	newest := map[string]node_file.NodeFile{}
	for _, f := range r.files {
		if f.NodeID != nodeID {
			continue
		}
		if cur, ok := newest[f.Path]; !ok || f.Version > cur.Version {
			newest[f.Path] = f
		}
	}
	out := make([]node_file.VersionMeta, 0, len(newest))
	for _, f := range newest {
		out = append(out, node_file.VersionMeta{
			Path: f.Path, Version: f.Version, Status: f.Status,
			Hash: f.Hash, UpdatedAt: f.UpdatedAt,
		})
	}
	return out, nil
}

func (r *fakeRepo) SumContentBytesByAccountID(_ context.Context, _ uuid.UUID) (int64, error) {
	var total int64
	for _, f := range r.files {
		total += int64(len(f.Content))
	}
	return total, nil
}

// A device report is bounded by the node's byte budget exactly like an
// operator write: the per-version cap times pruned history times the file cap
// multiplies far past BytesPerNode, and every byte arrives from something
// holding only a node's adoption token.
func TestAgentReportRespectsNodeByteBudget(t *testing.T) {
	svc, repo := newTestService()
	svc.SetLimits(node_file.Limits{VersionsPerPath: 20, FilesPerNode: 50, BytesPerNode: 1024})
	ctx := context.Background()
	nodeID := uuid.New()

	// Occupy most of the budget with an existing tracked version.
	big := make([]byte, 900)
	for i := range big {
		big[i] = 'a'
	}
	sum := sha256.Sum256(big)
	repo.files = append(repo.files, node_file.NodeFile{
		ID: uuid.New(), NodeID: nodeID, Path: "/etc/hosts",
		Content: string(big), Hash: hex.EncodeToString(sum[:]), Version: 1,
		Status: node_file.StatusApplied,
	})

	// A fresh report for the same tracked path that would overflow the budget
	// must be refused.
	over := make([]byte, 200)
	for i := range over {
		over[i] = 'b'
	}
	overSum := sha256.Sum256(over)
	_, err := svc.CreateOrUpdateFromAgent(ctx, nodeID, "/etc/hosts", string(over), hex.EncodeToString(overSum[:]), 0, node_file.StatusApplied, nil)
	if !errors.Is(err, node_file.ErrTrackedLimit) {
		t.Fatalf("err = %v, want ErrTrackedLimit", err)
	}

	// A small report that fits still lands.
	small := []byte("ok")
	smallSum := sha256.Sum256(small)
	if _, err := svc.CreateOrUpdateFromAgent(ctx, nodeID, "/etc/hosts", string(small), hex.EncodeToString(smallSum[:]), 0, node_file.StatusApplied, nil); err != nil {
		t.Fatalf("a report within the budget must be accepted: %v", err)
	}
}

// A saved version of a file the access reconciler writes is never applied,
// and the refusal leaves the version as it was rather than "applying".
func TestApplyRefusesAnAccessManagedFile(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	nodeID := uuid.New()
	const grant = "/etc/sudoers.d/90-orchestrator-ops"
	if _, err := svc.CreateVersion(ctx, nodeID, grant, "ops ALL=(ALL) NOPASSWD:ALL\n"); err != nil {
		t.Fatalf("create version: %v", err)
	}
	stored, _ := repo.GetLatestByNodeIDAndPath(ctx, nodeID, grant)
	before := stored.Status

	if _, err := svc.ApplyFileVersion(ctx, stored.ID); !errors.Is(err, node_file.ErrAccessManagedPath) {
		t.Fatalf("err = %v, want ErrAccessManagedPath", err)
	}
	after, _ := repo.GetLatestByNodeIDAndPath(ctx, nodeID, grant)
	if after.Status != before {
		t.Fatalf("status moved from %q to %q on a refused apply", before, after.Status)
	}
}
