package agentpkg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
	local_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/local"
)

func TestResolveLatestPrefersTheMarkerAndFallsBackToTheListing(t *testing.T) {
	ctx := context.Background()
	store, err := local_storage.NewLocalStorageClient(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	listed := []string{"latest", "0.8.0", "0.7.9"}

	if got := agentpkg.ResolveLatest(ctx, store, "agent-artifacts", listed); got != "0.8.0" {
		t.Fatalf("no marker: %q, want the newest listed version", got)
	}

	put := func(body string) {
		t.Helper()
		if _, err := store.StoreWithOptions(ctx, "agent-artifacts/"+agentpkg.LatestMarkerName, strings.NewReader(body), nil); err != nil {
			t.Fatal(err)
		}
	}
	put("0.7.9\n")
	if got := agentpkg.ResolveLatest(ctx, store, "agent-artifacts", listed); got != "0.7.9" {
		t.Fatalf("marker: %q, want 0.7.9", got)
	}

	// A marker that does not hold a version is not trusted.
	put("../../etc\n")
	if _, err := agentpkg.ReadLatestMarker(ctx, store, "agent-artifacts"); err == nil {
		t.Fatal("a malformed marker was accepted")
	}
	if got := agentpkg.ResolveLatest(ctx, store, "agent-artifacts", listed); got != "0.8.0" {
		t.Fatalf("malformed marker: %q, want the listing's answer", got)
	}

	if got := agentpkg.ResolveLatest(ctx, nil, "agent-artifacts", []string{"latest"}); got != "" {
		t.Fatalf("nothing known: %q", got)
	}
}

// `latest` is resolved per architecture.
// The architecture's own marker wins; the legacy marker is believed only for
// an architecture that lists the version it names; else the newest version
// listed for that architecture.
func TestResolveLatestForArch(t *testing.T) {
	ctx := context.Background()
	store, err := local_storage.NewLocalStorageClient(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		t.Helper()
		if _, err := store.StoreWithOptions(ctx, "agent-artifacts/"+name, strings.NewReader(body), nil); err != nil {
			t.Fatal(err)
		}
	}
	arm := []string{"latest", "0.8.0", "0.7.9"}
	amd := []string{"latest", "0.7.9", "0.7.8"}

	if got := agentpkg.ResolveLatestForArch(ctx, store, "agent-artifacts", "amd64", amd); got != "0.7.9" {
		t.Fatalf("no marker: %q, want amd64's newest listed", got)
	}
	// The legacy marker from an arm64-only release names a version amd64
	// does not have: not believed for amd64, believed for arm64.
	put(agentpkg.LatestMarkerName, "0.8.0\n")
	if got := agentpkg.ResolveLatestForArch(ctx, store, "agent-artifacts", "amd64", amd); got != "0.7.9" {
		t.Fatalf("legacy marker for an unlisted version: %q, want 0.7.9", got)
	}
	if got := agentpkg.ResolveLatestForArch(ctx, store, "agent-artifacts", "arm64", arm); got != "0.8.0" {
		t.Fatalf("legacy marker for a listed version: %q, want 0.8.0", got)
	}
	// The architecture's own marker wins over both.
	put(agentpkg.LatestMarkerNameFor("amd64"), "0.7.8\n")
	if got := agentpkg.ResolveLatestForArch(ctx, store, "agent-artifacts", "amd64", amd); got != "0.7.8" {
		t.Fatalf("arch marker: %q, want 0.7.8", got)
	}
	if got := agentpkg.ResolveLatestForArch(ctx, store, "agent-artifacts", "../x", amd); got != "0.7.9" {
		t.Fatalf("a malformed arch must not address another object: %q", got)
	}
	byArch := agentpkg.ResolveLatestByArch(ctx, store, "agent-artifacts", map[string][]string{"arm64": arm, "amd64": amd})
	if byArch["arm64"] != "0.8.0" || byArch["amd64"] != "0.7.8" {
		t.Fatalf("by arch = %v", byArch)
	}
}

func TestVersionsByArch(t *testing.T) {
	got := agentpkg.VersionsByArch([]string{
		"orchestrator-agent_0.7.9_linux_arm64.deb",
		"orchestrator-agent_0.8.0_linux_arm64.deb",
		"orchestrator-agent_latest_linux_arm64.deb",
		"orchestrator-agent_0.7.9_linux_amd64.deb",
		"orchestrator-watcher_0.9.0_linux_amd64.deb",
		"latest.arm64.version",
	})
	if strings.Join(got["arm64"], ",") != "latest,0.8.0,0.7.9" || strings.Join(got["amd64"], ",") != "0.7.9" || len(got) != 2 {
		t.Fatalf("VersionsByArch = %v", got)
	}
}
