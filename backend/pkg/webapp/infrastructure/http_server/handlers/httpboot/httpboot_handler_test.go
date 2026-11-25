package httpboot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

type fakeLookup struct {
	n   *node.Node
	err error
}

func (f fakeLookup) GetByHardwareSerialGlobal(_ context.Context, _ string) (*node.Node, error) {
	return f.n, f.err
}

type fakeResolver struct {
	policy organizational_unit.EffectivePolicy
	err    error
}

func (f fakeResolver) ResolveEffectivePolicy(_ context.Context, _, _ uuid.UUID) (organizational_unit.EffectivePolicy, error) {
	return f.policy, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newHandler builds a handler with a temp boot dir containing boot.img/boot.sig,
// unless bootDir is explicitly overridden to "".
func newHandler(t *testing.T, lookup NodeBySerialLookup, resolver EffectivePolicyResolver, configureDir bool) (*Handler, string) {
	t.Helper()
	dir := ""
	if configureDir {
		dir = t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "boot.img"), []byte("IMGCONTENT"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "boot.sig"), []byte("SIGCONTENT"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Provisioning.HTTPBootDir = dir
	// These existing tests exercise the pre-adoption behavior of a LAN install,
	// which is fail-open (unknown serial / error -> serve). The default is
	// fail-closed and is covered by the dedicated tests below.
	cfg.Provisioning.HTTPBootFailOpen = true
	return NewHandler(lookup, resolver, nil, cfg, discardLogger()), dir
}

// failClosedHandler builds a handler in the default fail-closed mode.
func failClosedHandler(t *testing.T, lookup NodeBySerialLookup, resolver EffectivePolicyResolver) *Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot.img"), []byte("IMGCONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Provisioning.HTTPBootDir = dir
	cfg.Provisioning.HTTPBootFailOpen = false
	return NewHandler(lookup, resolver, nil, cfg, discardLogger())
}

func do(t *testing.T, h *Handler, serial, file string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/http-boot/"+serial+"/"+file, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("serial", "file")
	c.SetParamValues(serial, file)
	if err := h.ServeBootFile(c); err != nil {
		t.Fatalf("ServeBootFile returned error: %v", err)
	}
	return rec
}

func policyWithBootToOS(v bool) organizational_unit.EffectivePolicy {
	return organizational_unit.EffectivePolicy{
		BootToOS: organizational_unit.ResolvedFlag{Value: v},
	}
}

// The ROM's User-Agent carries a firmware identity, and it is deliberately
// NEVER persisted (docs/HTTPBOOT-AUTH-RESEARCH.md, Phase 0): the serial in the
// URL is public, so any persisted value would be writable by anyone who knows
// one. The agent's authenticated gRPC report is the only writer of
// nodes.bootloader_version. This test pins the diagnostic-only path: a request
// carrying the UA must be served exactly like one without it.
func TestServeBootFile_BootloaderUAIsDiagnosticOnly(t *testing.T) {
	n := &node.Node{ID: uuid.New(), AccountID: uuid.New(), IPv4: "192.0.2.1"}
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)}, true)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/http-boot/10000000abcd/boot.img", nil)
	req.Header.Set("User-Agent", "rpiboot/1.0 (e041a0; version:f68405bc timestamp:1783585441)")
	w := httptest.NewRecorder()
	c := e.NewContext(req, w)
	c.SetParamNames("serial", "file")
	c.SetParamValues("10000000abcd", "boot.img")
	if err := h.ServeBootFile(c); err != nil {
		t.Fatalf("ServeBootFile returned error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 with a rpiboot UA present, got %d", w.Code)
	}
}

func TestServeBootFile_DisabledWhenNoDir(t *testing.T) {
	h, _ := newHandler(t, fakeLookup{err: app_errors.ErrNodeNotFound}, fakeResolver{}, false)
	rec := do(t, h, "10000000abcd", "boot.img")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when dir unset, got %d", rec.Code)
	}
}

func TestServeBootFile_RejectsBadFileAndSerial(t *testing.T) {
	h, _ := newHandler(t, fakeLookup{err: app_errors.ErrNodeNotFound}, fakeResolver{}, true)

	if rec := do(t, h, "10000000abcd", "passwd"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for disallowed file, got %d", rec.Code)
	}
	if rec := do(t, h, "../../etc", "boot.img"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for malformed serial, got %d", rec.Code)
	}
}

func TestServeBootFile_UnknownSerialServesBootstrap(t *testing.T) {
	h, _ := newHandler(t, fakeLookup{err: app_errors.ErrNodeNotFound}, fakeResolver{}, true)
	rec := do(t, h, "10000000abcd", "boot.img")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for unknown serial (new device), got %d", rec.Code)
	}
	if rec.Body.String() != "IMGCONTENT" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestServeBootFile_BootToOSWithholds(t *testing.T) {
	n := &node.Node{ID: uuid.New(), AccountID: uuid.New()}
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(true)}, true)
	rec := do(t, h, "10000000abcd", "boot.img")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 (fall through to NVMe) when boot_to_os=on, got %d", rec.Code)
	}
}

func TestServeBootFile_BootToOSOffServes(t *testing.T) {
	n := &node.Node{ID: uuid.New(), AccountID: uuid.New()}
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)}, true)
	rec := do(t, h, "10000000abcd", "boot.sig")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (netboot bootstrap) when boot_to_os=off, got %d", rec.Code)
	}
	if rec.Body.String() != "SIGCONTENT" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestServeBootFile_LookupErrorServes(t *testing.T) {
	// A transient lookup error must not strand a device; default to serving.
	h, _ := newHandler(t, fakeLookup{err: context.DeadlineExceeded}, fakeResolver{}, true)
	rec := do(t, h, "10000000abcd", "boot.img")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 on transient lookup error, got %d", rec.Code)
	}
}

// --- Fail-closed (default) mode ---

func TestServeBootFile_FailClosed_UnknownSerial404(t *testing.T) {
	h := failClosedHandler(t, fakeLookup{n: nil, err: app_errors.ErrNodeNotFound}, fakeResolver{})
	if rec := do(t, h, "10000000abcd", "boot.img"); rec.Code != http.StatusNotFound {
		t.Fatalf("fail-closed: want 404 for unknown serial, got %d", rec.Code)
	}
}

// A serial that matches nodes in more than one account is refused, but it is
// not a probe: striking for it would let one account's row ban the site where
// another account's device keeps asking for its own path.
func TestServeBootFile_FailClosed_AmbiguousSerialIsRefusedWithoutAStrike(t *testing.T) {
	ambiguous := failClosedHandler(t, fakeLookup{err: app_errors.ErrHardwareSerialAmbiguous}, fakeResolver{})
	if rec := do(t, ambiguous, "abcd0001", "boot.sig"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an ambiguous serial, got %d", rec.Code)
	}
	if strikes := ambiguous.guard.strikeCount("192.0.2.1"); strikes != 0 {
		t.Fatalf("an ambiguous serial struck the site %d time(s), want 0", strikes)
	}

	unknown := failClosedHandler(t, fakeLookup{err: app_errors.ErrNodeNotFound}, fakeResolver{})
	do(t, unknown, "abcd0001", "boot.sig")
	if strikes := unknown.guard.strikeCount("192.0.2.1"); strikes != 1 {
		t.Fatalf("an unknown serial struck %d time(s), want 1", strikes)
	}
}

func TestServeBootFile_FailClosed_LookupError404(t *testing.T) {
	h := failClosedHandler(t, fakeLookup{n: nil, err: errors.New("db down")}, fakeResolver{})
	if rec := do(t, h, "10000000abcd", "boot.img"); rec.Code != http.StatusNotFound {
		t.Fatalf("fail-closed: want 404 on lookup error, got %d", rec.Code)
	}
}

func TestServeBootFile_FailClosed_PolicyError404(t *testing.T) {
	known := &node.Node{ID: uuid.New(), AccountID: uuid.New(), Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd"}}
	h := failClosedHandler(t, fakeLookup{n: known}, fakeResolver{err: errors.New("policy boom")})
	if rec := do(t, h, "10000000abcd", "boot.img"); rec.Code != http.StatusNotFound {
		t.Fatalf("fail-closed: want 404 on policy error, got %d", rec.Code)
	}
}

func TestServeBootFile_FailClosed_KnownNodeServes(t *testing.T) {
	known := &node.Node{ID: uuid.New(), AccountID: uuid.New(), Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd"}}
	// boot_to_os off -> netboot the bootstrap even in fail-closed mode.
	pol := organizational_unit.EffectivePolicy{}
	pol.BootToOS.Value = false
	h := failClosedHandler(t, fakeLookup{n: known}, fakeResolver{policy: pol})
	if rec := do(t, h, "10000000abcd", "boot.img"); rec.Code != http.StatusOK {
		t.Fatalf("fail-closed: want 200 for a known netboot node, got %d", rec.Code)
	}
}

// Short serials, both halves.
//
// `serialRegex` must not accept a serial short enough for the global lookup's
// suffix fallback to resolve it to the newest node in a hex bucket across
// accounts, and a suffix hit must be visible, because the ban guard strikes
// only when a lookup FAILS and a suffix probe succeeds.
//
// The fallback's >= 8-character floor (node_repository.go) is the first bound.
// These two cover the rest: the pattern in front of it, and the log line.

func TestSerialRegexAcceptsRealSerialsAndRejectsProbes(t *testing.T) {
	accept := []string{
		"10000000abcd0001", // a full SoC serial: every real one is exactly
		"10000000abcd0007", // 16 lowercase hex
		"10000000ABCD0001", // raw path segment, so uppercase must pass; it is
		"abcd0001",         // normalized later. 8 = the WebUSB low word.
	}
	for _, s := range accept {
		if !serialRegex.MatchString(s) {
			t.Errorf("serial %q must be accepted — a real device sends this", s)
		}
	}

	reject := []string{
		"f",                 // one character, one hex bucket
		"ff",                //
		"abcd000",           // 7 — below the WebUSB low word
		"10000000abcd00019", // 17 — longer than a SoC serial
		"10000000abcd000g",  // non-hex
		"../../etc",         // traversal
		"10000000-abcd0001", // separators were allowed by the old regex
		"",                  //
	}
	for _, s := range reject {
		if serialRegex.MatchString(s) {
			t.Errorf("serial %q must be rejected", s)
		}
	}
}

func TestServeBootFile_SuffixMatchedSerialStrikesTheGuard(t *testing.T) {
	// The store resolved a node whose OWN serial differs from the one asked
	// for — which, since the exact-match arm compares the whole normalized
	// string, can only have come from the suffix fallback.
	n := &node.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"},
	}
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)}, true)

	if rec := do(t, h, "abcd0001", "boot.img"); rec.Code != http.StatusOK {
		// It still SERVES: the device gets its boot file. The point is that the
		// request is now recorded, not that it is refused — refusing would break
		// a legitimately short-serial node.
		t.Fatalf("want 200 (a suffix hit still serves), got %d", rec.Code)
	}
	if strikes := h.guard.strikeCount("192.0.2.1"); strikes != 1 {
		t.Fatalf("a suffix-matched hit must strike the guard exactly once, got %d", strikes)
	}
}

func TestServeBootFile_ExactSerialDoesNotStrike(t *testing.T) {
	// The ordinary case, and the one that must stay silent: every boot of every
	// healthy node asks for its own serial. Striking here would ban a cluster
	// for booting.
	n := &node.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"},
	}
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)}, true)

	if rec := do(t, h, "10000000ABCD0001", "boot.img"); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// Uppercase in the URL and lowercase in the store is the SAME serial, which
	// is why the comparison normalizes both sides rather than comparing raw.
	if strikes := h.guard.strikeCount("192.0.2.1"); strikes != 0 {
		t.Fatalf("an exact (case-insensitive) match must not strike, got %d", strikes)
	}
}

// nodeWithBootPath is a node whose agent read its EEPROM's HTTP_PATH back.
func nodeWithBootPath(serial, bootPath string) *node.Node {
	return &node.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: serial},
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			BootHTTPPath: bootPath,
		}}},
	}
}

func TestServeBootFile_SuffixMatchTheEEPROMAsksForDoesNotStrike(t *testing.T) {
	// A WebUSB-flashed node keeps asking for the low-word path its EEPROM was
	// baked with after it rebinds to the full serial. Its own readback names
	// that path, so the request is the device's normal boot: a site of such
	// nodes rebooting together must not ban its own address.
	n := nodeWithBootPath("10000000abcd0001", "http-boot/abcd0001")
	h, _ := newHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)}, true)

	for _, file := range []string{"boot.sig", "boot.img"} {
		if rec := do(t, h, "abcd0001", file); rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", file, rec.Code)
		}
	}
	if strikes := h.guard.strikeCount("192.0.2.1"); strikes != 0 {
		t.Fatalf("a suffix hit on the path the node's EEPROM asks for must not strike, got %d", strikes)
	}
}

// A bootloader baked from a WebUSB harvest asks with the low word of the
// node's own serial. While the node's readback is unknown that request is the
// legacy short form, not a device asking for a serial that is not its own: it
// is logged at INFO naming the cure, and still struck, since nothing tells it
// apart from a probe for that low word. Any other suffix, or the low word
// against a readback that names another path, stays a warning.
func TestServeBootFile_ShortFormOfTheNodesOwnSerialIsNotAWarning(t *testing.T) {
	cases := []struct {
		name      string
		n         *node.Node
		requested string
		wantWarn  bool
	}{
		{"low word, readback unknown", &node.Node{ID: uuid.New(), AccountID: uuid.New(),
			Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}}, "abcd0001", false},
		{"low word, readback names the full serial", nodeWithBootPath("10000000abcd0001", "http-boot/10000000abcd0001"), "abcd0001", true},
		{"a longer suffix", &node.Node{ID: uuid.New(), AccountID: uuid.New(),
			Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}}, "00abcd0001", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h, _ := newHandler(t, fakeLookup{n: tc.n}, fakeResolver{policy: policyWithBootToOS(false)}, true)
			h.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			if rec := do(t, h, tc.requested, "boot.sig"); rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			warned := strings.Contains(logs.String(), "level=WARN") &&
				strings.Contains(logs.String(), "a serial that is not its own")
			if warned != tc.wantWarn {
				t.Fatalf("warned = %v, want %v:\n%s", warned, tc.wantWarn, logs.String())
			}
			if !tc.wantWarn && !strings.Contains(logs.String(), "short (low-word) form") {
				t.Fatalf("the short form is not named as such:\n%s", logs.String())
			}
			if strikes := h.guard.strikeCount("192.0.2.1"); strikes != 1 {
				t.Fatalf("a suffix hit without a matching readback must still strike once, got %d", strikes)
			}
		})
	}
}

type fakeInstallLookup struct {
	inFlight bool
	err      error
	calls    int
}

func (f *fakeInstallLookup) OSInstallInFlight(context.Context, uuid.UUID) (bool, error) {
	f.calls++
	return f.inFlight, f.err
}

func TestServeBootFile_UnfinishedInstallServesRecoveryOverBootToOS(t *testing.T) {
	// boot_to_os=on would withhold, sending the device to its disk. With an OS
	// install to that disk unfinished, the disk holds a partial image: a boot
	// request now means the device was reset mid-write, and it must come back
	// to recovery instead.
	n := &node.Node{ID: uuid.New(), AccountID: uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}}
	h := failClosedHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(true)})
	if err := os.WriteFile(filepath.Join(h.cfg.Provisioning.HTTPBootDir, "boot.sig"), []byte("SIGCONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := &fakeInstallLookup{inFlight: true}
	h.SetOSInstallInFlightLookup(install)

	for _, file := range []string{"boot.sig", "boot.img"} {
		if rec := do(t, h, "10000000abcd0001", file); rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200 (recovery served during an unfinished install), got %d", file, rec.Code)
		}
	}

	// Once the install has settled, boot_to_os decides again.
	install.inFlight = false
	if rec := do(t, h, "10000000abcd0001", "boot.sig"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 (boot_to_os=on withholds) with no install in flight, got %d", rec.Code)
	}

	// A lookup error falls back to the policy decision.
	install.err = errors.New("db down")
	install.inFlight = true
	if rec := do(t, h, "10000000abcd0001", "boot.sig"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 on an install lookup error, got %d", rec.Code)
	}
}

func TestServeBootFile_InstallCheckIsNotAskedWhenServingAnyway(t *testing.T) {
	n := &node.Node{ID: uuid.New(), AccountID: uuid.New(),
		Fingerprint: &node.HardwareFingerprint{HardwareSerial: "10000000abcd0001"}}
	h := failClosedHandler(t, fakeLookup{n: n}, fakeResolver{policy: policyWithBootToOS(false)})
	install := &fakeInstallLookup{}
	h.SetOSInstallInFlightLookup(install)
	if rec := do(t, h, "10000000abcd0001", "boot.img"); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if install.calls != 0 {
		t.Fatalf("install lookup asked %d times on a serve decision, want 0", install.calls)
	}
}
