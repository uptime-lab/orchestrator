package node_file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// A FileChangedEvent is DEVICE-authenticated input — anything
// holding a node's adoption token can send one — and it used to bypass every
// node_file guard: no path policy, no size cap, no hash verification, and it
// could invent rows under arbitrary paths, invisible in the UI and
// unreclaimable through any API, exhausting the node's storage budget.

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestAgentReportRefusesARefusedPath(t *testing.T) {
	svc, repo := newTestService()

	content := "secret material"
	_, err := svc.CreateOrUpdateFromAgent(context.Background(), uuid.New(),
		"/etc/shadow", content, hashOf(content), 0, node_file.StatusApplied, nil)
	if err == nil {
		t.Fatal("a refused path must be refused from the device too")
	}
	if len(repo.files) != 0 {
		t.Fatal("no row may be created for a refused path")
	}
}

func TestAgentReportRefusesOversizedContent(t *testing.T) {
	svc, repo := newTestService()

	oversized := strings.Repeat("x", node_file.MaxContentBytes+1)
	_, err := svc.CreateOrUpdateFromAgent(context.Background(), uuid.New(),
		testPath, oversized, hashOf(oversized), 0, node_file.StatusApplied, nil)
	if err == nil {
		t.Fatal("content above the per-version cap must be refused")
	}
	if len(repo.files) != 0 {
		t.Fatal("no row may be created for oversized content")
	}
}

func TestAgentReportRefusesAMismatchedHash(t *testing.T) {
	svc, repo := newTestService()

	_, err := svc.CreateOrUpdateFromAgent(context.Background(), uuid.New(),
		testPath, testContent, hashOf("something else entirely"), 0, node_file.StatusApplied, nil)
	if err == nil {
		t.Fatal("a hash that does not match the content must be refused — it is the identity later logic dedupes on")
	}
	if len(repo.files) != 0 {
		t.Fatal("no row may be created for a mismatched hash")
	}
}

func TestAgentReportCannotInventRowsForUntrackedPaths(t *testing.T) {
	svc, repo := newTestService()

	// A perfectly allowed path — but nothing tracks it and the platform does
	// not watch it, so a device must not be able to create its first row.
	content := "device-invented"
	_, err := svc.CreateOrUpdateFromAgent(context.Background(), uuid.New(),
		"/etc/hosts", content, hashOf(content), 0, node_file.StatusApplied, nil)
	if err == nil {
		t.Fatal("a device must not create the first version of an untracked, non-platform path")
	}
	if len(repo.files) != 0 {
		t.Fatal("no row may be created for a device-invented path")
	}
}

func TestAgentReportStillCreatesPlatformWatchedPaths(t *testing.T) {
	svc, repo := newTestService()
	nodeID := uuid.New()

	// The agent's own config.txt report — version 0, first sighting — is the
	// legitimate flow the platform watch list exists for.
	got, err := svc.CreateOrUpdateFromAgent(context.Background(), nodeID,
		testPath, testContent, testHash, 0, node_file.StatusApplied, nil)
	if err != nil {
		t.Fatalf("a platform-watched path must still be creatable from the device: %v", err)
	}
	if got.Version != 1 || len(repo.files) != 1 {
		t.Fatalf("expected one v1 row, got %+v", repo.files)
	}
}

func TestAgentReportStillUpdatesTrackedPaths(t *testing.T) {
	svc, repo := newTestService()
	nodeID := uuid.New()

	// Operator tracks /etc/hosts first; the device may then report against it.
	if _, err := svc.CreateVersion(context.Background(), nodeID, "/etc/hosts", "127.0.0.1 localhost\n"); err != nil {
		t.Fatalf("track: %v", err)
	}
	newContent := "127.0.0.1 localhost\n10.0.0.1 lab\n"
	if _, err := svc.CreateOrUpdateFromAgent(context.Background(), nodeID,
		"/etc/hosts", newContent, hashOf(newContent), 0, node_file.StatusApplied, nil); err != nil {
		t.Fatalf("a tracked path must accept device reports: %v", err)
	}
	if len(repo.files) != 2 {
		t.Fatalf("expected the report to add v2, got %+v", repo.files)
	}
}
