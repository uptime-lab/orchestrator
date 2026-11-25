package cdn

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	local_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/local"
)

// The listing tells the browser what `latest` installs, so a version imported
// without moving `latest` is not mistaken for it.
func TestListAgentVersionsReportsWhatLatestInstalls(t *testing.T) {
	ctx := context.Background()
	store, err := local_storage.NewLocalStorageClient(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"orchestrator-agent_latest_linux_arm64.deb",
		"orchestrator-agent_0.8.0_linux_arm64.deb",
		"orchestrator-agent_0.7.9_linux_arm64.deb",
		"latest.version",
	} {
		body := "x"
		if name == "latest.version" {
			body = "0.7.9\n"
		}
		if _, err := store.StoreWithOptions(ctx, "agent-artifacts/"+name, strings.NewReader(body), nil); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Storage.AgentCDNBasePath = "agent-artifacts"
	h := NewCDNHandler(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/cdn/agent-versions", nil), rec)
	if err := h.ListAgentVersions(c); err != nil {
		t.Fatal(err)
	}
	var got AgentVersionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Latest != "0.7.9" || len(got.Versions) != 3 || got.Versions[1] != "0.8.0" {
		t.Fatalf("got %+v; want latest 0.7.9 with 0.8.0 still listed", got)
	}
}

// A release imported with --set-latest for
// arm64 alone moved arm64's `latest` only. The legacy marker named the new
// version for every architecture, so amd64 nodes read as outdated while each
// update reinstalled the older package. The answer is per architecture now,
// and `latest` keeps meaning arm64 for the deployed SPA.
func TestListAgentVersionsAnswersLatestPerArchitecture(t *testing.T) {
	ctx := context.Background()
	store, err := local_storage.NewLocalStorageClient(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"orchestrator-agent_latest_linux_arm64.deb": "x",
		"orchestrator-agent_0.8.0_linux_arm64.deb":  "x",
		"orchestrator-agent_0.7.9_linux_arm64.deb":  "x",
		"orchestrator-agent_latest_linux_amd64.deb": "x",
		"orchestrator-agent_0.7.9_linux_amd64.deb":  "x",
		"orchestrator-agent_0.7.8_linux_amd64.deb":  "x",
		// What an arm64-only --set-latest left: the legacy marker naming the
		// new version for every architecture, and arm64's own marker.
		"latest.version":       "0.8.0\n",
		"latest.arm64.version": "0.8.0\n",
	} {
		if _, err := store.StoreWithOptions(ctx, "agent-artifacts/"+name, strings.NewReader(body), nil); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Storage.AgentCDNBasePath = "agent-artifacts"
	h := NewCDNHandler(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/cdn/agent-versions", nil), rec)
	if err := h.ListAgentVersions(c); err != nil {
		t.Fatal(err)
	}
	var got AgentVersionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Latest != "0.8.0" {
		t.Errorf("latest = %q, want arm64's 0.8.0", got.Latest)
	}
	if got.LatestByArch["arm64"] != "0.8.0" || got.LatestByArch["amd64"] != "0.7.9" {
		t.Errorf("latest_by_arch = %v, want arm64 0.8.0 and amd64 0.7.9", got.LatestByArch)
	}
}
