package server_info

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// TestGetInstallScriptSignatureVerification renders the served install.sh and
// asserts it embeds the trusted signing key(s), verifies the package signature
// before installing, leaves no unreplaced placeholders, and is valid POSIX sh.
func TestGetInstallScriptSignatureVerification(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AdvertisementURL = "https://example.test"
	h := NewHandler(cfg, true, slog.Default())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	rec := httptest.NewRecorder()
	if err := h.GetInstallScript(e.NewContext(req, rec)); err != nil {
		t.Fatalf("GetInstallScript: %v", err)
	}
	body := rec.Body.String()

	if strings.Contains(body, "__PLATFORM_URL__") || strings.Contains(body, "__AGENT_SIGNING_PUBKEYS__") {
		t.Fatal("a placeholder was left unreplaced in install.sh")
	}
	for _, want := range []string{
		"-----BEGIN PUBLIC KEY-----",
		"openssl dgst -sha384 -verify",
		"refusing to install",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh is missing %q", want)
		}
	}

	// POSIX sh syntax check of the fully-rendered script (catches shell mistakes in
	// the injected verify block).
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendered install.sh failed `sh -n`: %v\n%s", err, out)
	}
}

// Re-running the adoption command must not redo everything: a multi-MB
// download, a signature verification, and the same package reinstalled as root.
// The script fetches the package's .sig first and skips the package work when a
// previous successful run recorded the same signature and the package is still
// installed.
//
// The properties asserted here are the ones that keep that safe, and every one of
// them is a way the shortcut could have gone wrong:
//
//   - the skip is gated on the package being INSTALLED, not just on the marker
//     existing, so a purged agent reinstalls;
//   - the marker is written from the bytes on disk, never from the value the CDN
//     reported, so one bad fetch cannot suppress every future install;
//   - the adoption file and the restart stay OUTSIDE the skip, because a second
//     run with a different token is how a device moves account, and a re-run on a
//     wedged agent is how it gets kicked;
//   - the only path that installs still verifies the signature.
func TestInstallScriptSkipsRedundantPackageWork(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AdvertisementURL = "https://example.test"
	h := NewHandler(cfg, true, slog.Default())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	rec := httptest.NewRecorder()
	if err := h.GetInstallScript(e.NewContext(req, rec)); err != nil {
		t.Fatalf("GetInstallScript: %v", err)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"$DEB_URL.sig",                    // the cheap probe, on a route that exists
		"install ok installed",            // gated on the package really being there
		"cmp -s \"$TMP_SIG\"",             // compared byte for byte with the recorded copy
		"skipping download",               // and it says so
		"cp \"$TMP_SIG\" \"$SIG_MARKER\"", // recorded only after a verified install
	} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh is missing %q", want)
		}
	}

	// The restart and the adoption write must not be inside the skipped block.
	// Checked by position: the block closes before them.
	skipEnd := strings.Index(body, "systemctl restart orchestrator-agent")
	tokenWrite := strings.Index(body, "/etc/orchestrator/node-adoption.txt")
	pkgSkip := strings.Index(body, "skipping download")
	if pkgSkip < 0 || tokenWrite < 0 || skipEnd < 0 {
		t.Fatal("could not locate the skip block and the steps that must follow it")
	}
	if tokenWrite < pkgSkip || skipEnd < pkgSkip {
		t.Error("the adoption write or the restart moved above the package skip; a re-run with a new token would then do nothing")
	}

	// Still valid POSIX sh with the new branch in place — this one runs as root
	// through curl|sh, so a syntax slip is a broken install for every new device.
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendered install.sh failed `sh -n`: %v\n%s", err, out)
	}
}

// `sh -n` proves the script PARSES; it cannot prove the script means what was
// intended. An escaping slip of this kind passes the syntax check: a mistake
// that leaves `${Status}` unquoted in
//
//	dpkg-query -W -f='${Status}' orchestrator-agent
//
// lets the SHELL expand it, the node prints "Status: parameter not set", the
// installed-package test silently evaluates false, and the skip this is all for
// never happens. Valid shell, wrong program.
//
// The artifact is recognisable: '"'"' is the way one escapes a single quote
// inside a single-quoted SHELL string, and it has no business in a Go raw string,
// where a single quote is just a character.
func TestInstallScriptHasNoLeakedShellQuoting(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AdvertisementURL = "https://example.test"
	h := NewHandler(cfg, true, slog.Default())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	rec := httptest.NewRecorder()
	if err := h.GetInstallScript(e.NewContext(req, rec)); err != nil {
		t.Fatalf("GetInstallScript: %v", err)
	}
	body := rec.Body.String()

	if strings.Contains(body, `'"'"'`) {
		t.Error(`the rendered script contains '"'"' — shell single-quote escaping ` +
			`leaked into the Go raw string, which leaves the value it wraps unquoted`)
	}
	// The two dpkg-query format strings must reach the device single-quoted, or
	// the shell eats them.
	for _, want := range []string{`-f='${Status}'`, `-f='${Version}'`} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh must pass %s through to dpkg-query verbatim", want)
		}
	}
}
