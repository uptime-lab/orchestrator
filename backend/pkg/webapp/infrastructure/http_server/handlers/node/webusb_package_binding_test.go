package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/handlers/httpboot"
)

// layoutSigner mirrors eeprom-signer/server.py's write: the package lands in
// by-serial/<serial>/ for a request without a node id (the legacy layout) and
// in by-serial/<serial>/<node_id>/ for one that names its node. Every file
// carries the adoption blob it was baked with, so a fetch shows whose
// credential it returned.
func layoutSigner(t *testing.T, bootDir string) *httptest.Server {
	t.Helper()
	nodeIDRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		rel := "eeprom/by-serial/" + req["serial"]
		if nodeIDRe.MatchString(req["node_id"]) {
			rel += "/" + req["node_id"]
		}
		dir := filepath.Join(bootDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Error(err)
		}
		files := []string{"recovery.bin", "pieeprom.upd", "pieeprom.sig", "SHA256SUMS"}
		blob, derr := base64.StdEncoding.DecodeString(req["adoption_b64"])
		if derr != nil {
			t.Error(derr)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), blob, 0o644); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eepromsigner.SignResponse{
			OK: true, Serial: req["serial"], Path: rel, Files: files,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// perAccountAdoption hands each node a distinct adoption blob so the served
// bytes name their owner.
type perAccountAdoption struct {
	webusbFakeService
}

func (f *perAccountAdoption) GetAdoptionInfo(_ context.Context, accountID, nodeID uuid.UUID, _, _ string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte("token-of-" + accountID.String() + "-" + nodeID.String())), nil
}

type noNodeLookup struct{}

func (noNodeLookup) GetByHardwareSerialGlobal(context.Context, string) (*nodeDomain.Node, error) {
	return nil, nil
}

type noPolicy struct{}

func (noPolicy) ResolveEffectivePolicy(context.Context, uuid.UUID, uuid.UUID) (organizational_unit.EffectivePolicy, error) {
	return organizational_unit.EffectivePolicy{}, nil
}

// fetchPackage drives ServeEepromPackage the way the router does: the wildcard
// is everything after /net_install/eeprom/, the capability rides the query.
func fetchPackage(t *testing.T, h *httpboot.Handler, base, sigQuery, file string) *httptest.ResponseRecorder {
	t.Helper()
	p := base + "/" + file
	req := httptest.NewRequest(http.MethodGet, p+sigQuery, nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("*")
	c.SetParamValues(strings.TrimPrefix(p, "/net_install/eeprom/"))
	if err := h.ServeEepromPackage(c); err != nil {
		t.Fatalf("ServeEepromPackage: %v", err)
	}
	return rec
}

// Two accounts bake the SAME short serial (a WebUSB low word legitimately
// repeats across devices, so the cross-account guard exempts it). Each
// capability must open its OWN node's package and nothing else: account B,
// holding a capability for the serial, must not read the package account A
// baked afterwards, which carries A's node credential.
func TestWebUSBPackageCapabilityIsBoundToTheNode(t *testing.T) {
	const short = "abcdef12"
	bootDir := t.TempDir()
	signer := layoutSigner(t, bootDir)

	cfg := &config.Config{}
	cfg.Secrets.CDNSigningKey = "test-cdn-signing-key-for-the-binding-test"
	cfg.Provisioning.HTTPBootDir = bootDir

	provision := func(account uuid.UUID) WebUSBProvisionResponse {
		t.Helper()
		svc := &perAccountAdoption{webusbFakeService{ownerAccount: uuid.New()}}
		h := &NodeHandler{
			nodeService: svc,
			signer:      eepromsigner.NewClient(signer.URL, ""),
			logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			config:      cfg,
		}
		rec := webusbRequest(t, h, account, uuid.New(), short)
		if rec.Code != http.StatusOK {
			t.Fatalf("provision for an account: status %d body %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data WebUSBProvisionResponse `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env.Data
	}

	accountB, accountA := uuid.New(), uuid.New()
	b := provision(accountB) // the squatter bakes first and keeps its capability
	a := provision(accountA) // the victim bakes its own device afterwards

	boot := httpboot.NewHandler(noNodeLookup{}, noPolicy{}, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	recB := fetchPackage(t, boot, b.PackageBase, b.SigQuery, "pieeprom.upd")
	if strings.Contains(recB.Body.String(), accountA.String()) {
		t.Fatalf("account B's capability returned account A's package: %q", recB.Body.String())
	}
	if recB.Code != http.StatusOK || !strings.Contains(recB.Body.String(), "token-of-"+accountB.String()+"-"+b.NodeID) {
		t.Fatalf("account B's capability: status %d body %q, want B's own package", recB.Code, recB.Body.String())
	}

	recA := fetchPackage(t, boot, a.PackageBase, a.SigQuery, "pieeprom.upd")
	if recA.Code != http.StatusOK || !strings.Contains(recA.Body.String(), "token-of-"+accountA.String()+"-"+a.NodeID) {
		t.Fatalf("account A's capability: status %d body %q, want A's own package", recA.Code, recA.Body.String())
	}
}
