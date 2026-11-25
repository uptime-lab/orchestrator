package httpboot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
)

func eepromHandler(t *testing.T) (*Handler, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Provisioning.HTTPBootDir = dir
	cfg.Secrets.CDNSigningKey = "test-cdn-signing-key-eeprom-package"
	return NewHandler(fakeLookup{}, fakeResolver{}, nil, cfg, discardLogger()), dir, cdnsign.DeriveKey(cfg.CDNSigningSecret())
}

func bake(t *testing.T, dir string, rel ...string) {
	t.Helper()
	d := filepath.Join(append([]string{dir, "eeprom", "by-serial"}, rel...)...)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "pieeprom.upd"), []byte(strings.Join(rel, "/")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func getPackage(t *testing.T, h *Handler, serial, exp, sig string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{cdnsign.ParamExpires: {exp}, cdnsign.ParamSignature: {sig}}
	req := httptest.NewRequest(http.MethodGet, "/net_install/eeprom/by-serial/"+serial+"/pieeprom.upd?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("*")
	c.SetParamValues("by-serial/" + serial + "/pieeprom.upd")
	if err := h.ServeEepromPackage(c); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A per-device package is served only from the directory of the node its
// capability names; every other capability gets the same 404.
func TestEepromPackageServesOnlyTheNodeTheCapabilityNames(t *testing.T) {
	const serial = "abcdef12"
	h, dir, key := eepromHandler(t)
	accountA, nodeA := uuid.New(), uuid.New()
	accountB, nodeB := uuid.New(), uuid.New()
	bake(t, dir, serial, nodeA.String())
	bake(t, dir, serial, nodeB.String())
	bake(t, dir, serial) // a legacy serial-only package left from before the binding

	exp, sig := cdnsign.SignEEPROMPackage(serial, accountA, nodeA, key, time.Minute)
	if rec := getPackage(t, h, serial, exp, sig); rec.Code != http.StatusOK || rec.Body.String() != serial+"/"+nodeA.String() {
		t.Fatalf("own capability: status %d body %q", rec.Code, rec.Body.String())
	}

	// B's capability opens B's package, never A's.
	expB, sigB := cdnsign.SignEEPROMPackage(serial, accountB, nodeB, key, time.Minute)
	if rec := getPackage(t, h, serial, expB, sigB); rec.Body.String() != serial+"/"+nodeB.String() {
		t.Fatalf("B's capability served %q", rec.Body.String())
	}

	// Re-pointing B's signature at A's node breaks the HMAC.
	forged := accountB.String() + "." + nodeA.String() + "." + sigB[strings.LastIndex(sigB, ".")+1:]
	if rec := getPackage(t, h, serial, expB, forged); rec.Code != http.StatusNotFound {
		t.Fatalf("forged node: status %d, want 404", rec.Code)
	}

	// A path-only capability (the pre-binding shape) opens nothing, the legacy
	// serial-directory package included.
	expP, sigP := cdnsign.Sign("by-serial/"+serial, key, time.Minute)
	if rec := getPackage(t, h, serial, expP, sigP); rec.Code != http.StatusNotFound {
		t.Fatalf("path-only capability: status %d, want 404", rec.Code)
	}

	// A valid capability for a node with no package is the same 404.
	expC, sigC := cdnsign.SignEEPROMPackage(serial, uuid.New(), uuid.New(), key, time.Minute)
	if rec := getPackage(t, h, serial, expC, sigC); rec.Code != http.StatusNotFound {
		t.Fatalf("no package: status %d, want 404", rec.Code)
	}

	// A capability for another serial does not verify here.
	expS, sigS := cdnsign.SignEEPROMPackage("abcdef13", accountA, nodeA, key, time.Minute)
	if rec := getPackage(t, h, serial, expS, sigS); rec.Code != http.StatusNotFound {
		t.Fatalf("other serial: status %d, want 404", rec.Code)
	}
}
